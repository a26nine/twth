package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type comparisonKind uint8

const (
	compareSuccess comparisonKind = iota
	compareBatch
	compareRPCError
	compareNotification
)

type observedRPCResponse struct {
	statusCode int
	mediaType  string
	body       []byte
}

type rpcResponsePair struct {
	direct  observedRPCResponse
	proxied observedRPCResponse
}

type liveRPCSettings struct {
	enabled  bool
	upstream *url.URL
}

type rpcAnchor struct {
	number string
	hash   string
}

type liveRPCCase struct {
	name          string
	body          []byte
	kind          comparisonKind
	wantErrorCode int64
	validate      func(observedRPCResponse) error
}

type rpcProgressLogger func(format string, args ...any)

type transientRPCError struct {
	err error
}

func (e transientRPCError) Error() string { return e.err.Error() }
func (e transientRPCError) Unwrap() error { return e.err }

func loadLiveRPCSettings(lookup func(string) (string, bool)) (liveRPCSettings, error) {
	liveValue, liveSet := lookup("RPC_LIVE")
	if !liveSet {
		return liveRPCSettings{}, nil
	}
	if liveValue != "1" {
		return liveRPCSettings{}, errors.New("RPC_LIVE must be unset or exactly 1")
	}

	upstreamValue := "https://polygon.drpc.org"
	if value, ok := lookup("RPC_UPSTREAM_URL"); ok {
		upstreamValue = value
	}
	upstream, err := url.Parse(upstreamValue)
	if err != nil {
		return liveRPCSettings{}, errors.New("RPC_UPSTREAM_URL is invalid")
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return liveRPCSettings{}, errors.New("RPC_UPSTREAM_URL scheme must be http or https")
	}
	if upstream.Host == "" {
		return liveRPCSettings{}, errors.New("RPC_UPSTREAM_URL must include a host")
	}
	if upstream.RawQuery != "" || upstream.ForceQuery || strings.Contains(upstreamValue, "#") {
		return liveRPCSettings{}, errors.New("RPC_UPSTREAM_URL must not include a query or fragment")
	}
	return liveRPCSettings{enabled: true, upstream: upstream}, nil
}

func requestRPCPair(
	ctx context.Context,
	client *http.Client,
	upstreamURL *url.URL,
	proxyURL *url.URL,
	body []byte,
) (rpcResponsePair, error) {
	direct, err := requestRPC(ctx, client, upstreamURL, body)
	if err != nil {
		return rpcResponsePair{}, fmt.Errorf("direct RPC request: %w", err)
	}
	proxied, err := requestRPC(ctx, client, proxyURL, body)
	if err != nil {
		return rpcResponsePair{}, fmt.Errorf("proxied RPC request: %w", err)
	}
	return rpcResponsePair{direct: direct, proxied: proxied}, nil
}

func requestRPC(
	ctx context.Context,
	client *http.Client,
	endpoint *url.URL,
	body []byte,
) (observedRPCResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return observedRPCResponse{}, errors.New("create RPC request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		message := "RPC transport failed"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "RPC transport timed out"
		}
		return observedRPCResponse{}, transientRPCError{err: errors.New(message)}
	}
	return readObservedRPCResponse(response, 2<<20)
}

func selectRPCAnchor(ctx context.Context, client *http.Client, upstreamURL *url.URL) (rpcAnchor, error) {
	heightPayload, err := marshalRPCRequest(1, "eth_blockNumber", []any{})
	if err != nil {
		return rpcAnchor{}, err
	}
	heightResponse, err := requestDirectRPCWithRetry(ctx, client, upstreamURL, heightPayload)
	if err != nil {
		return rpcAnchor{}, fmt.Errorf("select anchor height: %w", err)
	}
	height, err := rpcStringResult(heightResponse, 1)
	if err != nil {
		return rpcAnchor{}, fmt.Errorf("decode anchor height: %w", err)
	}
	if !strings.HasPrefix(height, "0x") || len(height) == 2 {
		return rpcAnchor{}, errors.New("anchor height is not a hexadecimal quantity")
	}
	heightNumber, ok := new(big.Int).SetString(height[2:], 16)
	if !ok {
		return rpcAnchor{}, errors.New("anchor height is not a hexadecimal quantity")
	}
	depth := big.NewInt(16)
	if heightNumber.Cmp(depth) < 0 {
		return rpcAnchor{}, errors.New("chain height is below the 16-block anchor depth")
	}
	heightNumber.Sub(heightNumber, depth)
	anchorNumber := "0x" + heightNumber.Text(16)
	hash, err := fetchRPCAnchorHash(ctx, client, upstreamURL, anchorNumber)
	if err != nil {
		return rpcAnchor{}, err
	}
	return rpcAnchor{number: anchorNumber, hash: hash}, nil
}

func fetchRPCAnchorHash(ctx context.Context, client *http.Client, upstreamURL *url.URL, number string) (string, error) {
	payload, err := marshalRPCRequest(2, "eth_getBlockByNumber", []any{number, false})
	if err != nil {
		return "", err
	}
	response, err := requestDirectRPCWithRetry(ctx, client, upstreamURL, payload)
	if err != nil {
		return "", fmt.Errorf("fetch anchor block: %w", err)
	}
	value, err := rpcResult(response, 2)
	if err != nil {
		return "", fmt.Errorf("decode anchor block: %w", err)
	}
	block, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("anchor block result is not an object")
	}
	blockNumber, ok := block["number"].(string)
	if !ok || blockNumber != number {
		return "", errors.New("anchor block number does not match request")
	}
	hash, ok := block["hash"].(string)
	if !ok || hash == "" {
		return "", errors.New("anchor block hash is missing")
	}
	return hash, nil
}

func requestDirectRPCWithRetry(
	ctx context.Context,
	client *http.Client,
	upstreamURL *url.URL,
	body []byte,
) (observedRPCResponse, error) {
	pair, err := retryRPCPair(ctx, func(ctx context.Context) (rpcResponsePair, error) {
		response, err := requestRPC(ctx, client, upstreamURL, body)
		return rpcResponsePair{direct: response, proxied: response}, err
	}, nil)
	if err != nil {
		return observedRPCResponse{}, err
	}
	return pair.direct, nil
}

func marshalRPCRequest(id int, method string, params any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal RPC request: %w", err)
	}
	return payload, nil
}

func rpcStringResult(response observedRPCResponse, wantID int) (string, error) {
	result, err := rpcResult(response, wantID)
	if err != nil {
		return "", err
	}
	value, ok := result.(string)
	if !ok {
		return "", errors.New("RPC result is not a string")
	}
	return value, nil
}

func rpcResult(response observedRPCResponse, wantID int) (any, error) {
	if response.statusCode != http.StatusOK {
		return nil, fmt.Errorf("RPC HTTP status = %d, want 200", response.statusCode)
	}
	if response.mediaType != "application/json" {
		return nil, fmt.Errorf("RPC media type = %q, want application/json", response.mediaType)
	}
	value, err := decodeRPCJSON(response.body)
	if err != nil {
		return nil, err
	}
	object, err := rpcObject(value, "RPC")
	if err != nil {
		return nil, err
	}
	if object["jsonrpc"] != "2.0" {
		return nil, errors.New("RPC JSON-RPC version is not 2.0")
	}
	wantJSONID := json.Number(fmt.Sprintf("%d", wantID))
	if !reflect.DeepEqual(object["id"], wantJSONID) {
		return nil, errors.New("RPC response id does not match request")
	}
	result, ok := object["result"]
	if !ok {
		return nil, errors.New("RPC response is missing result")
	}
	return result, nil
}

func runRPCCompatibilityMatrix(
	ctx context.Context,
	client *http.Client,
	upstreamURL *url.URL,
	proxyURL *url.URL,
	anchor rpcAnchor,
	logf rpcProgressLogger,
) error {
	cases, err := buildLiveRPCCases(anchor)
	if err != nil {
		return err
	}
	for _, testCase := range cases {
		logRPCProgress(logf, "rpc case started name=%s", testCase.name)
		started := time.Now()
		attempts := 0
		pair, err := retryRPCPair(ctx, func(ctx context.Context) (rpcResponsePair, error) {
			attempts++
			return requestRPCPair(ctx, client, upstreamURL, proxyURL, testCase.body)
		}, nil)
		duration := time.Since(started).Round(time.Millisecond)
		if err != nil {
			logRPCProgress(logf, "rpc case failed name=%s attempts=%d duration=%s", testCase.name, attempts, duration)
			return fmt.Errorf("%s: %w", testCase.name, err)
		}
		if err := compareRPCResponses(testCase.kind, testCase.wantErrorCode, pair.direct, pair.proxied); err != nil {
			logRPCProgress(logf, "rpc case failed name=%s direct_status=%d proxied_status=%d attempts=%d duration=%s", testCase.name, pair.direct.statusCode, pair.proxied.statusCode, attempts, duration)
			return fmt.Errorf("%s: %w", testCase.name, err)
		}
		if testCase.validate != nil {
			if err := testCase.validate(pair.direct); err != nil {
				logRPCProgress(logf, "rpc case failed name=%s direct_status=%d proxied_status=%d attempts=%d duration=%s", testCase.name, pair.direct.statusCode, pair.proxied.statusCode, attempts, duration)
				return fmt.Errorf("%s: %w", testCase.name, err)
			}
		}
		logRPCProgress(logf, "rpc case passed name=%s direct_status=%d proxied_status=%d attempts=%d duration=%s", testCase.name, pair.direct.statusCode, pair.proxied.statusCode, attempts, duration)
	}
	return nil
}

func logRPCProgress(logf rpcProgressLogger, format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}

func buildLiveRPCCases(anchor rpcAnchor) ([]liveRPCCase, error) {
	chainID, err := marshalRPCRequest(11, "eth_chainId", []any{})
	if err != nil {
		return nil, err
	}
	block, err := marshalRPCRequest(12, "eth_getBlockByNumber", []any{anchor.number, false})
	if err != nil {
		return nil, err
	}
	balance, err := marshalRPCRequest(13, "eth_getBalance", []any{
		"0x0000000000000000000000000000000000000000",
		anchor.number,
	})
	if err != nil {
		return nil, err
	}
	batch, err := json.Marshal([]map[string]any{
		{"jsonrpc": "2.0", "id": 41, "method": "eth_chainId", "params": []any{}},
		{"jsonrpc": "2.0", "id": 42, "method": "eth_getBlockTransactionCountByNumber", "params": []any{anchor.number}},
		{"jsonrpc": "2.0", "id": 43, "method": "eth_getBalance", "params": []any{
			"0x0000000000000000000000000000000000000000",
			anchor.number,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal batch RPC request: %w", err)
	}
	unknownMethod, err := marshalRPCRequest(51, "twth_testUnknownMethod", []any{})
	if err != nil {
		return nil, err
	}
	notification, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "eth_chainId",
		"params":  []any{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal notification RPC request: %w", err)
	}

	return []liveRPCCase{
		{
			name: "eth_chainId",
			body: chainID,
			kind: compareSuccess,
			validate: func(response observedRPCResponse) error {
				chainID, err := rpcStringResult(response, 11)
				if err != nil {
					return err
				}
				if chainID != "0x89" {
					return fmt.Errorf("chain ID = %q, want 0x89", chainID)
				}
				return nil
			},
		},
		{
			name: "eth_getBlockByNumber",
			body: block,
			kind: compareSuccess,
			validate: func(response observedRPCResponse) error {
				return validateAnchorBlock(response, 12, anchor)
			},
		},
		{
			name: "eth_getBalance",
			body: balance,
			kind: compareSuccess,
		},
		{
			name: "three-item batch",
			body: batch,
			kind: compareBatch,
		},
		{
			name:          "unknown method",
			body:          unknownMethod,
			kind:          compareRPCError,
			wantErrorCode: -32601,
		},
		{
			name: "truncated JSON",
			body: []byte(`{"jsonrpc":"2.0",`),
			kind: compareRPCError,
		},
		{
			name: "notification",
			body: notification,
			kind: compareNotification,
		},
	}, nil
}

func validateAnchorBlock(response observedRPCResponse, wantID int, anchor rpcAnchor) error {
	result, err := rpcResult(response, wantID)
	if err != nil {
		return err
	}
	block, ok := result.(map[string]any)
	if !ok {
		return errors.New("anchor block result is not an object")
	}
	if block["number"] != anchor.number {
		return errors.New("anchor block number does not match selected anchor")
	}
	if block["hash"] != anchor.hash {
		return errors.New("anchor block hash does not match selected anchor")
	}
	return nil
}

func runStableRPCMatrix(
	ctx context.Context,
	selectAnchor func(context.Context) (rpcAnchor, error),
	runMatrix func(context.Context, rpcAnchor) error,
	fetchHash func(context.Context, string) (string, error),
	logf rpcProgressLogger,
) error {
	for anchorAttempt := 1; anchorAttempt <= 2; anchorAttempt++ {
		anchor, err := selectAnchor(ctx)
		if err != nil {
			return fmt.Errorf("select RPC anchor: %w", err)
		}
		logRPCProgress(logf, "rpc anchor selected block=%s attempt=%d", anchor.number, anchorAttempt)
		matrixErr := runMatrix(ctx, anchor)
		currentHash, err := fetchHash(ctx, anchor.number)
		if err != nil {
			return fmt.Errorf("verify RPC anchor: %w", err)
		}
		if currentHash != anchor.hash {
			if anchorAttempt == 2 {
				return errors.New("RPC anchor changed twice")
			}
			logRPCProgress(logf, "rpc anchor changed block=%s; selecting a new anchor", anchor.number)
			continue
		}
		if matrixErr != nil {
			return matrixErr
		}
		logRPCProgress(logf, "rpc anchor confirmed block=%s", anchor.number)
		return nil
	}
	panic("unreachable")
}

func TestLiveRPCCompatibility(t *testing.T) {
	settings, err := loadLiveRPCSettings(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.enabled {
		t.Skip("set RPC_LIVE=1 to run live RPC compatibility checks")
	}

	proxyServer := httptest.NewServer(NewHandler(Options{
		Upstream:        settings.upstream,
		Transport:       NewTransport(5 * time.Second),
		MaxRequestBytes: 1 << 20,
		Logger:          testLogger(),
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("parse local proxy URL: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	err = runStableRPCMatrix(
		ctx,
		func(ctx context.Context) (rpcAnchor, error) {
			return selectRPCAnchor(ctx, client, settings.upstream)
		},
		func(ctx context.Context, anchor rpcAnchor) error {
			return runRPCCompatibilityMatrix(ctx, client, settings.upstream, proxyURL, anchor, t.Logf)
		},
		func(ctx context.Context, number string) (string, error) {
			return fetchRPCAnchorHash(ctx, client, settings.upstream, number)
		},
		t.Logf,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("rpc compatibility suite passed cases=7")
}

func compareRPCResponses(kind comparisonKind, wantErrorCode int64, direct, proxied observedRPCResponse) error {
	if direct.statusCode != proxied.statusCode {
		return fmt.Errorf("HTTP status mismatch: direct=%d proxied=%d", direct.statusCode, proxied.statusCode)
	}
	if kind == compareNotification {
		return compareNotificationResponses(direct, proxied)
	}
	if direct.mediaType != proxied.mediaType {
		return fmt.Errorf("media type mismatch: direct=%q proxied=%q", direct.mediaType, proxied.mediaType)
	}
	if direct.mediaType != "application/json" {
		return fmt.Errorf("unexpected JSON media type %q", direct.mediaType)
	}

	directValue, err := decodeRPCJSON(direct.body)
	if err != nil {
		return fmt.Errorf("decode direct JSON response: %w", err)
	}
	proxiedValue, err := decodeRPCJSON(proxied.body)
	if err != nil {
		return fmt.Errorf("decode proxied JSON response: %w", err)
	}

	switch kind {
	case compareSuccess:
		return compareSuccessValues(directValue, proxiedValue)
	case compareBatch:
		return compareBatchValues(directValue, proxiedValue)
	case compareRPCError:
		return compareErrorValues(wantErrorCode, directValue, proxiedValue)
	default:
		return fmt.Errorf("unsupported comparison kind %d", kind)
	}
}

func compareNotificationResponses(direct, proxied observedRPCResponse) error {
	directEmpty := len(direct.body) == 0
	proxiedEmpty := len(proxied.body) == 0
	if directEmpty && proxiedEmpty {
		return nil
	}
	if directEmpty != proxiedEmpty {
		return errors.New("notification response-body presence mismatch")
	}
	if direct.mediaType != proxied.mediaType {
		return fmt.Errorf("media type mismatch: direct=%q proxied=%q", direct.mediaType, proxied.mediaType)
	}
	if direct.mediaType != "application/json" {
		return fmt.Errorf("unexpected JSON media type %q", direct.mediaType)
	}
	directValue, err := decodeRPCJSON(direct.body)
	if err != nil {
		return fmt.Errorf("decode direct notification response: %w", err)
	}
	proxiedValue, err := decodeRPCJSON(proxied.body)
	if err != nil {
		return fmt.Errorf("decode proxied notification response: %w", err)
	}
	return compareSuccessValues(directValue, proxiedValue)
}

func decodeRPCJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func compareSuccessValues(directValue, proxiedValue any) error {
	direct, err := rpcObject(directValue, "direct")
	if err != nil {
		return err
	}
	proxied, err := rpcObject(proxiedValue, "proxied")
	if err != nil {
		return err
	}
	if err := compareEnvelope(direct, proxied); err != nil {
		return err
	}
	directResult, directOK := direct["result"]
	proxiedResult, proxiedOK := proxied["result"]
	if !directOK || !proxiedOK {
		return errors.New("success response is missing result")
	}
	if _, ok := direct["error"]; ok {
		return errors.New("direct success response contains error")
	}
	if _, ok := proxied["error"]; ok {
		return errors.New("proxied success response contains error")
	}
	if !reflect.DeepEqual(directResult, proxiedResult) {
		return errors.New("result mismatch")
	}
	return nil
}

func compareBatchValues(directValue, proxiedValue any) error {
	directItems, ok := directValue.([]any)
	if !ok {
		return errors.New("direct batch response is not an array")
	}
	proxiedItems, ok := proxiedValue.([]any)
	if !ok {
		return errors.New("proxied batch response is not an array")
	}
	directByID, err := indexBatchByID(directItems, "direct")
	if err != nil {
		return err
	}
	proxiedByID, err := indexBatchByID(proxiedItems, "proxied")
	if err != nil {
		return err
	}
	if len(directByID) != len(proxiedByID) {
		return fmt.Errorf("batch response count mismatch: direct=%d proxied=%d", len(directByID), len(proxiedByID))
	}
	for id, directItem := range directByID {
		proxiedItem, ok := proxiedByID[id]
		if !ok {
			return fmt.Errorf("proxied batch response is missing id %s", id)
		}
		if err := compareSuccessValues(directItem, proxiedItem); err != nil {
			return fmt.Errorf("batch response id %s: %w", id, err)
		}
	}
	return nil
}

func indexBatchByID(items []any, source string) (map[string]any, error) {
	indexed := make(map[string]any, len(items))
	for _, item := range items {
		object, err := rpcObject(item, source+" batch item")
		if err != nil {
			return nil, err
		}
		id, ok := object["id"]
		if !ok {
			return nil, fmt.Errorf("%s batch item is missing id", source)
		}
		encodedID, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("encode %s batch id: %w", source, err)
		}
		key := string(encodedID)
		if _, exists := indexed[key]; exists {
			return nil, fmt.Errorf("%s batch response contains duplicate id %s", source, key)
		}
		indexed[key] = object
	}
	return indexed, nil
}

func compareErrorValues(wantErrorCode int64, directValue, proxiedValue any) error {
	direct, err := rpcErrorObject(directValue, "direct")
	if err != nil {
		return err
	}
	proxied, err := rpcErrorObject(proxiedValue, "proxied")
	if err != nil {
		return err
	}
	if err := compareEnvelope(direct, proxied); err != nil {
		return err
	}
	directCode, err := rpcErrorCode(direct, "direct")
	if err != nil {
		return err
	}
	proxiedCode, err := rpcErrorCode(proxied, "proxied")
	if err != nil {
		return err
	}
	if directCode != proxiedCode {
		return fmt.Errorf("RPC error code mismatch: direct=%d proxied=%d", directCode, proxiedCode)
	}
	if wantErrorCode != 0 && directCode != wantErrorCode {
		return fmt.Errorf("direct RPC error code = %d, want %d", directCode, wantErrorCode)
	}
	return nil
}

func rpcErrorObject(value any, source string) (map[string]any, error) {
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) != 1 {
		return nil, fmt.Errorf("%s JSON-RPC error response is neither an object nor a single-item array", source)
	}
	return rpcObject(items[0], source+" JSON-RPC error item")
}

func rpcObject(value any, source string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s JSON-RPC response is not an object", source)
	}
	return object, nil
}

func compareEnvelope(direct, proxied map[string]any) error {
	if direct["jsonrpc"] != "2.0" || proxied["jsonrpc"] != "2.0" {
		return errors.New("JSON-RPC version is not 2.0")
	}
	if !reflect.DeepEqual(direct["id"], proxied["id"]) {
		return errors.New("JSON-RPC id mismatch")
	}
	return nil
}

func rpcErrorCode(response map[string]any, source string) (int64, error) {
	errorValue, ok := response["error"]
	if !ok {
		return 0, fmt.Errorf("%s response is missing error", source)
	}
	errorObject, ok := errorValue.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("%s RPC error is not an object", source)
	}
	code, ok := errorObject["code"].(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s RPC error code is not a number", source)
	}
	parsed, err := code.Int64()
	if err != nil {
		return 0, fmt.Errorf("parse %s RPC error code: %w", source, err)
	}
	return parsed, nil
}

func readObservedRPCResponse(response *http.Response, maxBytes int64) (observedRPCResponse, error) {
	defer response.Body.Close()
	if maxBytes <= 0 {
		return observedRPCResponse{}, errors.New("response byte limit must be positive")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return observedRPCResponse{}, fmt.Errorf("read RPC response: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return observedRPCResponse{}, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	mediaType := ""
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err = mime.ParseMediaType(contentType)
		if err != nil {
			return observedRPCResponse{}, fmt.Errorf("parse response Content-Type: %w", err)
		}
	}
	return observedRPCResponse{
		statusCode: response.StatusCode,
		mediaType:  mediaType,
		body:       body,
	}, nil
}

func retryRPCPair(
	ctx context.Context,
	attempt func(context.Context) (rpcResponsePair, error),
	wait func(context.Context, time.Duration) error,
) (rpcResponsePair, error) {
	if wait == nil {
		wait = waitForRetry
	}
	delays := [...]time.Duration{250 * time.Millisecond, 500 * time.Millisecond}
	for attemptNumber := 1; attemptNumber <= 3; attemptNumber++ {
		pair, err := attempt(ctx)
		transient := isTransientRPCPair(pair, err)
		if !transient {
			return pair, err
		}
		if attemptNumber == 3 {
			if err != nil {
				return rpcResponsePair{}, fmt.Errorf("transient RPC failure after 3 attempts: %w", err)
			}
			return rpcResponsePair{}, errors.New("transient RPC HTTP status after 3 attempts")
		}
		if err := wait(ctx, delays[attemptNumber-1]); err != nil {
			return rpcResponsePair{}, fmt.Errorf("wait before RPC retry: %w", err)
		}
	}
	panic("unreachable")
}

func isTransientRPCPair(pair rpcResponsePair, err error) bool {
	if err != nil {
		var transient transientRPCError
		return errors.As(err, &transient)
	}
	return isTransientStatus(pair.direct.statusCode) || isTransientStatus(pair.proxied.statusCode)
}

func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestCompareRPCResponses(t *testing.T) {
	tests := []struct {
		name          string
		kind          comparisonKind
		wantErrorCode int64
		direct        observedRPCResponse
		proxied       observedRPCResponse
		wantError     bool
	}{
		{
			name: "success ignores object key order",
			kind: compareSuccess,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":{"number":"0x10","hash":"0xabc"}}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"result":{"hash":"0xabc","number":"0x10"},"id":1,"jsonrpc":"2.0"}`),
			},
		},
		{
			name: "success result mismatch",
			kind: compareSuccess,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"direct-value-that-must-not-be-dumped"}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"proxied-value-that-must-not-be-dumped"}`),
			},
			wantError: true,
		},
		{
			name: "status mismatch",
			kind: compareSuccess,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusCreated,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`),
			},
			wantError: true,
		},
		{
			name: "media type mismatch",
			kind: compareSuccess,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "text/plain",
				body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`),
			},
			wantError: true,
		},
		{
			name: "batch ignores response order",
			kind: compareBatch,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":41,"result":"0x89"},{"jsonrpc":"2.0","id":42,"result":"0x2"}]`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"id":42,"result":"0x2","jsonrpc":"2.0"},{"id":41,"result":"0x89","jsonrpc":"2.0"}]`),
			},
		},
		{
			name: "batch missing id",
			kind: compareBatch,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":41,"result":"0x89"}]`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","result":"0x89"}]`),
			},
			wantError: true,
		},
		{
			name: "batch duplicate id",
			kind: compareBatch,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":41,"result":"0x89"},{"jsonrpc":"2.0","id":42,"result":"0x2"}]`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":41,"result":"0x89"},{"jsonrpc":"2.0","id":41,"result":"0x2"}]`),
			},
			wantError: true,
		},
		{
			name:          "rpc error ignores message and data",
			kind:          compareRPCError,
			wantErrorCode: -32601,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32601,"message":"first","data":"one"}}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32601,"message":"second","data":"two"}}`),
			},
		},
		{
			name: "rpc error accepts provider single-item array",
			kind: compareRPCError,
			direct: observedRPCResponse{
				statusCode: http.StatusBadRequest,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"first parse message"}}]`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusBadRequest,
				mediaType:  "application/json",
				body:       []byte(`[{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"second parse message"}}]`),
			},
		},
		{
			name:          "rpc error code mismatch",
			kind:          compareRPCError,
			wantErrorCode: -32601,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32601,"message":"missing"}}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32602,"message":"params"}}`),
			},
			wantError: true,
		},
		{
			name:          "rpc error id mismatch",
			kind:          compareRPCError,
			wantErrorCode: -32601,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32601,"message":"missing"}}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":52,"error":{"code":-32601,"message":"missing"}}`),
			},
			wantError: true,
		},
		{
			name:          "rpc error must have expected code",
			kind:          compareRPCError,
			wantErrorCode: -32601,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32000,"message":"server"}}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":51,"error":{"code":-32000,"message":"server"}}`),
			},
			wantError: true,
		},
		{
			name: "notification accepts equal empty responses",
			kind: compareNotification,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
			},
		},
		{
			name: "notification accepts matching provider response",
			kind: compareNotification,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"jsonrpc":"2.0","id":null,"result":"0x89"}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
				mediaType:  "application/json",
				body:       []byte(`{"result":"0x89","id":null,"jsonrpc":"2.0"}`),
			},
		},
		{
			name: "notification rejects response body",
			kind: compareNotification,
			direct: observedRPCResponse{
				statusCode: http.StatusOK,
				body:       []byte(`{"jsonrpc":"2.0","result":"unexpected"}`),
			},
			proxied: observedRPCResponse{
				statusCode: http.StatusOK,
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := compareRPCResponses(tt.kind, tt.wantErrorCode, tt.direct, tt.proxied)
			if tt.wantError {
				if err == nil {
					t.Fatal("compareRPCResponses() error = nil, want non-nil")
				}
				for _, secret := range []string{"direct-value-that-must-not-be-dumped", "proxied-value-that-must-not-be-dumped"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("compareRPCResponses() error contains response body value %q", secret)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("compareRPCResponses() error = %v", err)
			}
		})
	}
}

func TestReadObservedRPCResponse(t *testing.T) {
	t.Run("normalizes media type and accepts exact limit", func(t *testing.T) {
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": {"application/json; charset=utf-8"},
			},
			Body: io.NopCloser(strings.NewReader("1234")),
		}

		got, err := readObservedRPCResponse(response, 4)
		if err != nil {
			t.Fatalf("readObservedRPCResponse() error = %v", err)
		}
		if got.statusCode != http.StatusOK || got.mediaType != "application/json" || string(got.body) != "1234" {
			t.Fatalf("readObservedRPCResponse() = %+v", got)
		}
	})

	t.Run("rejects response over limit", func(t *testing.T) {
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("12345")),
		}

		_, err := readObservedRPCResponse(response, 4)
		if err == nil || !strings.Contains(err.Error(), "response exceeds 4 bytes") {
			t.Fatalf("readObservedRPCResponse() error = %v", err)
		}
	})
}

func TestRetryRPCPair(t *testing.T) {
	t.Run("retries transient errors with configured backoff", func(t *testing.T) {
		attempts := 0
		var delays []time.Duration
		got, err := retryRPCPair(context.Background(), func(context.Context) (rpcResponsePair, error) {
			attempts++
			if attempts < 3 {
				return rpcResponsePair{}, transientRPCError{err: errors.New("network unavailable")}
			}
			return successfulRPCPair(), nil
		}, func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		})
		if err != nil {
			t.Fatalf("retryRPCPair() error = %v", err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
		if len(delays) != 2 || delays[0] != 250*time.Millisecond || delays[1] != 500*time.Millisecond {
			t.Fatalf("delays = %v, want [250ms 500ms]", delays)
		}
		if got.direct.statusCode != http.StatusOK || got.proxied.statusCode != http.StatusOK {
			t.Fatalf("retryRPCPair() = %+v", got)
		}
	})

	t.Run("retries transient status and stops after three attempts", func(t *testing.T) {
		attempts := 0
		_, err := retryRPCPair(context.Background(), func(context.Context) (rpcResponsePair, error) {
			attempts++
			return rpcResponsePair{
				direct:  observedRPCResponse{statusCode: http.StatusServiceUnavailable},
				proxied: observedRPCResponse{statusCode: http.StatusOK},
			}, nil
		}, func(context.Context, time.Duration) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
			t.Fatalf("retryRPCPair() error = %v", err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("does not retry non-transient error", func(t *testing.T) {
		attempts := 0
		_, err := retryRPCPair(context.Background(), func(context.Context) (rpcResponsePair, error) {
			attempts++
			return rpcResponsePair{}, errors.New("invalid response")
		}, func(context.Context, time.Duration) error {
			t.Fatal("wait called for non-transient error")
			return nil
		})
		if err == nil || attempts != 1 {
			t.Fatalf("retryRPCPair() error/attempts = %v/%d, want non-nil/1", err, attempts)
		}
	})
}

func TestLoadLiveRPCSettings(t *testing.T) {
	tests := []struct {
		name        string
		values      map[string]string
		wantEnabled bool
		wantURL     string
		wantError   string
	}{
		{name: "disabled when RPC_LIVE is unset"},
		{
			name:        "enabled with default upstream",
			values:      map[string]string{"RPC_LIVE": "1"},
			wantEnabled: true,
			wantURL:     "https://polygon.drpc.org",
		},
		{
			name: "enabled with upstream base path",
			values: map[string]string{
				"RPC_LIVE":         "1",
				"RPC_UPSTREAM_URL": "https://example.com/polygon/key",
			},
			wantEnabled: true,
			wantURL:     "https://example.com/polygon/key",
		},
		{
			name:      "rejects unexpected RPC_LIVE value",
			values:    map[string]string{"RPC_LIVE": "true"},
			wantError: "RPC_LIVE must be unset or exactly 1",
		},
		{
			name: "rejects upstream query without exposing it",
			values: map[string]string{
				"RPC_LIVE":         "1",
				"RPC_UPSTREAM_URL": "https://example.com/polygon?token=secret-value-must-not-leak",
			},
			wantError: "RPC_UPSTREAM_URL must not include a query or fragment",
		},
		{
			name: "rejects unsupported upstream scheme",
			values: map[string]string{
				"RPC_LIVE":         "1",
				"RPC_UPSTREAM_URL": "ftp://example.com/polygon",
			},
			wantError: "RPC_UPSTREAM_URL scheme must be http or https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings, err := loadLiveRPCSettings(mapLookup(tt.values))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("loadLiveRPCSettings() error = %v, want containing %q", err, tt.wantError)
				}
				if strings.Contains(err.Error(), "secret-value-must-not-leak") {
					t.Fatal("loadLiveRPCSettings() error exposes upstream query")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadLiveRPCSettings() error = %v", err)
			}
			if settings.enabled != tt.wantEnabled {
				t.Fatalf("enabled = %t, want %t", settings.enabled, tt.wantEnabled)
			}
			if tt.wantURL != "" && settings.upstream.String() != tt.wantURL {
				t.Fatalf("upstream = %q, want %q", settings.upstream, tt.wantURL)
			}
		})
	}
}

func TestRequestRPCPairUsesDirectAndProxyRoutes(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
			return
		}
		if !bytes.Equal(body, payload) {
			t.Errorf("upstream body differs")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`))
	}))
	defer upstream.Close()

	upstreamURL := mustParseCompatURL(t, upstream.URL)
	proxyServer := httptest.NewServer(NewHandler(Options{
		Upstream:        upstreamURL,
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	}))
	defer proxyServer.Close()

	pair, err := requestRPCPair(
		context.Background(),
		&http.Client{Timeout: time.Second},
		upstreamURL,
		mustParseCompatURL(t, proxyServer.URL),
		payload,
	)
	if err != nil {
		t.Fatalf("requestRPCPair() error = %v", err)
	}
	if err := compareRPCResponses(compareSuccess, 0, pair.direct, pair.proxied); err != nil {
		t.Fatalf("compareRPCResponses() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestSelectRPCAnchorSubtractsSixteenBlocks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case bytes.Contains(body, []byte(`"method":"eth_blockNumber"`)):
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x30"}`))
		case bytes.Contains(body, []byte(`"method":"eth_getBlockByNumber"`)):
			if !bytes.Contains(body, []byte(`"0x20"`)) {
				t.Errorf("block request does not contain 16-block anchor")
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"number":"0x20","hash":"0xanchor"}}`))
		default:
			t.Errorf("unexpected request body")
		}
	}))
	defer upstream.Close()

	anchor, err := selectRPCAnchor(
		context.Background(),
		&http.Client{Timeout: time.Second},
		mustParseCompatURL(t, upstream.URL),
	)
	if err != nil {
		t.Fatalf("selectRPCAnchor() error = %v", err)
	}
	if anchor.number != "0x20" || anchor.hash != "0xanchor" {
		t.Fatalf("selectRPCAnchor() = %+v, want number/hash 0x20/0xanchor", anchor)
	}
}

func TestRunRPCCompatibilityMatrix(t *testing.T) {
	anchor := rpcAnchor{number: "0x20", hash: "0xanchor"}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if bytes.Equal(body, []byte(`{"jsonrpc":"2.0",`)) {
			_, _ = w.Write([]byte(`[{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"could not parse request"}}]`))
			return
		}

		value, err := decodeRPCJSON(body)
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if batch, ok := value.([]any); ok {
			responses := make([]map[string]any, 0, len(batch))
			for _, item := range batch {
				request, ok := item.(map[string]any)
				if !ok {
					t.Errorf("batch request item is not an object")
					return
				}
				method, _ := request["method"].(string)
				result := any("0x0")
				switch method {
				case "eth_chainId":
					result = "0x89"
				case "eth_getBlockTransactionCountByNumber":
					result = "0x2"
				case "eth_getBalance":
					result = "0x0"
				default:
					t.Errorf("unexpected batch method %q", method)
					return
				}
				responses = append(responses, map[string]any{
					"jsonrpc": "2.0",
					"id":      request["id"],
					"result":  result,
				})
			}
			if call%2 == 0 {
				for left, right := 0, len(responses)-1; left < right; left, right = left+1, right-1 {
					responses[left], responses[right] = responses[right], responses[left]
				}
			}
			_ = json.NewEncoder(w).Encode(responses)
			return
		}

		request, ok := value.(map[string]any)
		if !ok {
			t.Errorf("request is not an object")
			return
		}
		method, _ := request["method"].(string)
		id, hasID := request["id"]
		if !hasID {
			w.WriteHeader(http.StatusOK)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": id}
		switch method {
		case "eth_chainId":
			response["result"] = "0x89"
		case "eth_getBlockByNumber":
			response["result"] = map[string]any{"number": anchor.number, "hash": anchor.hash}
		case "eth_getBalance":
			response["result"] = "0x0"
		case "twth_testUnknownMethod":
			message := "method missing from first backend"
			if call%2 == 0 {
				message = "method missing from second backend"
			}
			response["error"] = map[string]any{"code": -32601, "message": message}
		default:
			t.Errorf("unexpected method %q", method)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()

	upstreamURL := mustParseCompatURL(t, upstream.URL)
	proxyServer := httptest.NewServer(NewHandler(Options{
		Upstream:        upstreamURL,
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	}))
	defer proxyServer.Close()

	var progress []string
	err := runRPCCompatibilityMatrix(
		context.Background(),
		&http.Client{Timeout: time.Second},
		upstreamURL,
		mustParseCompatURL(t, proxyServer.URL),
		anchor,
		func(format string, args ...any) {
			progress = append(progress, fmt.Sprintf(format, args...))
		},
	)
	if err != nil {
		t.Fatalf("runRPCCompatibilityMatrix() error = %v", err)
	}
	if calls.Load() != 14 {
		t.Fatalf("upstream calls = %d, want 14", calls.Load())
	}
	logs := strings.Join(progress, "\n")
	if got := strings.Count(logs, "rpc case started"); got != 7 {
		t.Fatalf("started log count = %d, want 7; logs=%q", got, logs)
	}
	if got := strings.Count(logs, "rpc case passed"); got != 7 {
		t.Fatalf("passed log count = %d, want 7; logs=%q", got, logs)
	}
	for _, want := range []string{
		"name=eth_chainId",
		"direct_status=200",
		"proxied_status=200",
		"attempts=1",
		"duration=",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("progress logs do not contain %q: %q", want, logs)
		}
	}
	for _, forbidden := range []string{upstream.URL, `"result"`, "0x89", anchor.hash} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("progress logs contain sensitive response or endpoint value %q", forbidden)
		}
	}
}

func TestRunStableRPCMatrix(t *testing.T) {
	t.Run("retries once when anchor changes", func(t *testing.T) {
		anchors := []rpcAnchor{
			{number: "0x20", hash: "0xold"},
			{number: "0x30", hash: "0xstable"},
		}
		selectCalls := 0
		var matrixAnchors []rpcAnchor
		var progress []string
		err := runStableRPCMatrix(
			context.Background(),
			func(context.Context) (rpcAnchor, error) {
				anchor := anchors[selectCalls]
				selectCalls++
				return anchor, nil
			},
			func(_ context.Context, anchor rpcAnchor) error {
				matrixAnchors = append(matrixAnchors, anchor)
				if anchor.hash == "0xold" {
					return errors.New("result mismatch caused by changed anchor")
				}
				return nil
			},
			func(_ context.Context, number string) (string, error) {
				if number == "0x20" {
					return "0xnew", nil
				}
				return "0xstable", nil
			},
			func(format string, args ...any) {
				progress = append(progress, fmt.Sprintf(format, args...))
			},
		)
		if err != nil {
			t.Fatalf("runStableRPCMatrix() error = %v", err)
		}
		if selectCalls != 2 || len(matrixAnchors) != 2 || matrixAnchors[1].number != "0x30" {
			t.Fatalf("select calls/matrix anchors = %d/%+v", selectCalls, matrixAnchors)
		}
		logs := strings.Join(progress, "\n")
		for _, want := range []string{
			"rpc anchor selected block=0x20 attempt=1",
			"rpc anchor changed block=0x20",
			"rpc anchor selected block=0x30 attempt=2",
		} {
			if !strings.Contains(logs, want) {
				t.Errorf("progress logs do not contain %q: %q", want, logs)
			}
		}
		for _, forbidden := range []string{"0xold", "0xnew", "0xstable"} {
			if strings.Contains(logs, forbidden) {
				t.Errorf("progress logs contain anchor hash %q", forbidden)
			}
		}
	})

	t.Run("fails when anchor changes twice", func(t *testing.T) {
		selectCalls := 0
		err := runStableRPCMatrix(
			context.Background(),
			func(context.Context) (rpcAnchor, error) {
				selectCalls++
				return rpcAnchor{number: "0x20", hash: fmt.Sprintf("0xold%d", selectCalls)}, nil
			},
			func(context.Context, rpcAnchor) error { return nil },
			func(context.Context, string) (string, error) { return "0xchanged", nil },
			nil,
		)
		if err == nil || !strings.Contains(err.Error(), "anchor changed twice") {
			t.Fatalf("runStableRPCMatrix() error = %v", err)
		}
	})

	t.Run("returns matrix mismatch when anchor is stable", func(t *testing.T) {
		err := runStableRPCMatrix(
			context.Background(),
			func(context.Context) (rpcAnchor, error) {
				return rpcAnchor{number: "0x20", hash: "0xstable"}, nil
			},
			func(context.Context, rpcAnchor) error { return errors.New("semantic mismatch") },
			func(context.Context, string) (string, error) { return "0xstable", nil },
			nil,
		)
		if err == nil || !strings.Contains(err.Error(), "semantic mismatch") {
			t.Fatalf("runStableRPCMatrix() error = %v", err)
		}
	})
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func mustParseCompatURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return parsed
}

func successfulRPCPair() rpcResponsePair {
	response := observedRPCResponse{
		statusCode: http.StatusOK,
		mediaType:  "application/json",
		body:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`),
	}
	return rpcResponsePair{direct: response, proxied: response}
}
