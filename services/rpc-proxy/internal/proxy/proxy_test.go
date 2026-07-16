package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"twth/rpcproxy/internal/app"
	"twth/rpcproxy/internal/buildinfo"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return parsed
}

func TestProxyForwardsRequestAndResponse(t *testing.T) {
	type observedRequest struct {
		method          string
		path            string
		rawQuery        string
		forceQuery      bool
		host            string
		body            []byte
		endToEndHeader  string
		hopHeader       string
		xForwardedFor   string
		xForwardedHost  string
		xForwardedProto string
		forwarded       string
	}
	observed := make(chan observedRequest, 1)

	compressed := new(bytes.Buffer)
	zipWriter := gzip.NewWriter(compressed)
	_, _ = zipWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x89"}`))
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		observed <- observedRequest{
			method:          r.Method,
			path:            r.URL.EscapedPath(),
			rawQuery:        r.URL.RawQuery,
			forceQuery:      r.URL.ForceQuery,
			host:            r.Host,
			body:            body,
			endToEndHeader:  r.Header.Get("X-End-To-End"),
			hopHeader:       r.Header.Get("X-Hop"),
			xForwardedFor:   r.Header.Get("X-Forwarded-For"),
			xForwardedHost:  r.Header.Get("X-Forwarded-Host"),
			xForwardedProto: r.Header.Get("X-Forwarded-Proto"),
			forwarded:       r.Header.Get("Forwarded"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-Upstream", "preserved")
		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "remove")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer upstream.Close()

	target := parseURL(t, upstream.URL+"/base")
	handler := NewHandler(Options{
		Upstream:        target,
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	req := httptest.NewRequest(http.MethodPost, "http://client.example/rpc/slash", bytes.NewReader(payload))
	req.Host = "client.example"
	req.RemoteAddr = "192.0.2.10:4321"
	req.URL.RawPath = "/rpc%2Fslash"
	req.URL.RawQuery = "bad=1;still=2&ok=1"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-End-To-End", "keep")
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "remove")
	req.Header.Set("Forwarded", "for=203.0.113.99")
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	req.Header.Set("X-Forwarded-Host", "spoofed.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusCreated; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := res.Header().Get("X-Upstream"), "preserved"; got != want {
		t.Errorf("X-Upstream = %q, want %q", got, want)
	}
	if got, want := res.Header().Get("Content-Encoding"), "gzip"; got != want {
		t.Errorf("Content-Encoding = %q, want %q", got, want)
	}
	if got := res.Header().Get("X-Response-Hop"); got != "" {
		t.Errorf("X-Response-Hop = %q, want empty", got)
	}
	if got, want := res.Body.Bytes(), compressed.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("response body differs: got %x want %x", got, want)
	}

	got := <-observed
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/base/rpc%2Fslash" {
		t.Errorf("path = %q, want /base/rpc%%2Fslash", got.path)
	}
	if got.rawQuery != "bad=1;still=2&ok=1" {
		t.Errorf("RawQuery = %q, want %q", got.rawQuery, "bad=1;still=2&ok=1")
	}
	if got.host != target.Host {
		t.Errorf("Host = %q, want %q", got.host, target.Host)
	}
	if !bytes.Equal(got.body, payload) {
		t.Errorf("body = %q, want %q", got.body, payload)
	}
	if got.endToEndHeader != "keep" || got.hopHeader != "" {
		t.Errorf("end-to-end/hop headers = %q/%q, want keep/empty", got.endToEndHeader, got.hopHeader)
	}
	if got.xForwardedFor != "192.0.2.10" || got.xForwardedHost != "client.example" || got.xForwardedProto != "http" {
		t.Errorf("forwarded headers = %q %q %q", got.xForwardedFor, got.xForwardedHost, got.xForwardedProto)
	}
	if got.forwarded != "" {
		t.Errorf("Forwarded = %q, want empty", got.forwarded)
	}
}

func TestProxyPreservesMalformedRawQueryBeforeTransport(t *testing.T) {
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.RawQuery, "bad=%zz&ok=1"; got != want {
			t.Errorf("RawQuery = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode:    http.StatusNoContent,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
			Request:       r,
		}, nil
	})
	handler := NewHandler(Options{
		Upstream:        parseURL(t, "https://example.com/base"),
		Transport:       transport,
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodGet, "http://client.example/rpc", nil)
	req.URL.RawQuery = "bad=%zz&ok=1"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

type hijackTrackingResponseWriter struct {
	header   http.Header
	status   int
	hijacked bool
}

func (w *hijackTrackingResponseWriter) Header() http.Header {
	return w.header
}

func (w *hijackTrackingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func (w *hijackTrackingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *hijackTrackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	proxyConn, clientConn := net.Pipe()
	_ = clientConn.Close()
	return proxyConn, bufio.NewReadWriter(bufio.NewReader(proxyConn), bufio.NewWriter(proxyConn)), nil
}

func TestProxySuppressesProtocolUpgrades(t *testing.T) {
	var transportCalls int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		transportCalls++
		if got := r.Header.Get("Connection"); got != "" {
			t.Errorf("outbound Connection = %q, want empty", got)
		}
		if got := r.Header.Get("Upgrade"); got != "" {
			t.Errorf("outbound Upgrade = %q, want empty", got)
		}

		backendConn, backendPeer := net.Pipe()
		_ = backendPeer.Close()
		return &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Header: http.Header{
				"Connection": {"Upgrade"},
				"Upgrade":    {"websocket"},
			},
			Body:    backendConn,
			Request: r,
		}, nil
	})
	handler := NewHandler(Options{
		Upstream:        parseURL(t, "https://example.com"),
		Transport:       transport,
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/rpc", nil)
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	res := &hijackTrackingResponseWriter{header: make(http.Header)}

	handler.ServeHTTP(res, req)

	if transportCalls != 1 {
		t.Fatalf("transport calls = %d, want 1", transportCalls)
	}
	if res.hijacked {
		t.Fatal("response connection was hijacked")
	}
	if got, want := res.status, http.StatusBadGateway; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestProxyPreservesBareQuery(t *testing.T) {
	observed := make(chan bool, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.URL.ForceQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	handler := NewHandler(Options{
		Upstream:        parseURL(t, upstream.URL),
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodGet, "http://client.example/rpc", nil)
	req.URL.ForceQuery = true
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got := <-observed; !got {
		t.Fatal("upstream ForceQuery = false, want true")
	}
}

func TestProxyPassesRPCBodiesUnchanged(t *testing.T) {
	payloads := []string{
		`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`,
		`[{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}]`,
		`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`,
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer upstream.Close()

			handler := NewHandler(Options{
				Upstream:        parseURL(t, upstream.URL),
				Transport:       NewTransport(time.Second),
				MaxRequestBytes: 4096,
				Logger:          testLogger(),
			})
			req := httptest.NewRequest(http.MethodPost, "http://proxy.test/", strings.NewReader(payload))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if got := res.Body.String(); got != payload {
				t.Fatalf("body = %q, want %q", got, payload)
			}
		})
	}
}

func TestProxyPassesNonJSONBytesUnchanged(t *testing.T) {
	payload := []byte{0x00, 0xff, '{', 'n', 'o', 't', '-', 'j', 's', 'o', 'n', '}', '\n'}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	handler := NewHandler(Options{
		Upstream:        parseURL(t, upstream.URL),
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got := res.Body.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("body differs: got %x want %x", got, payload)
	}
}

func TestProxyPassesJSONRPCErrorResponseUnchanged(t *testing.T) {
	want := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(want)
	}))
	defer upstream.Close()

	handler := NewHandler(Options{
		Upstream:        parseURL(t, upstream.URL),
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://proxy.test/", strings.NewReader("{}")))

	if got := res.Body.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestProxyRejectsOversizedBodyBeforeUpstream(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
	}{
		{name: "known length", contentLength: 5},
		{name: "unknown length", contentLength: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamCalls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			handler := NewHandler(Options{
				Upstream:        parseURL(t, upstream.URL),
				Transport:       NewTransport(time.Second),
				MaxRequestBytes: 4,
				Logger:          testLogger(),
			})
			req := httptest.NewRequest(http.MethodPost, "http://proxy.test/", strings.NewReader("12345"))
			req.ContentLength = tt.contentLength
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if got, want := res.Code, http.StatusRequestEntityTooLarge; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got, want := res.Body.String(), "{\"error\":\"request body too large\"}\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if got := upstreamCalls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}
		})
	}
}

func TestProxyAcceptsSmallBodyWithMaxInt64Limit(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0"}`)
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body == nil {
			t.Error("outbound body is nil")
		} else {
			var err error
			body, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read outbound body: %v", err)
			}
		}
		if !bytes.Equal(body, payload) {
			t.Errorf("outbound body = %q, want %q", body, payload)
		}
		return &http.Response{
			StatusCode:    http.StatusNoContent,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
			Request:       r,
		}, nil
	})
	handler := NewHandler(Options{
		Upstream:        parseURL(t, "https://example.com"),
		Transport:       transport,
		MaxRequestBytes: math.MaxInt64,
		Logger:          testLogger(),
	})
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://proxy.test/", bytes.NewReader(payload)))

	if got, want := res.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestProxyClosesOriginalBodyAndSetsAcceptedContentLength(t *testing.T) {
	payload := []byte("accepted opaque bytes")
	originalBody := &closeTrackingBody{Reader: bytes.NewReader(payload)}
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !originalBody.closed {
			t.Error("original request body was not closed before transport")
		}
		if got, want := r.ContentLength, int64(len(payload)); got != want {
			t.Errorf("outbound ContentLength = %d, want %d", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read outbound body: %v", err)
		}
		if !bytes.Equal(body, payload) {
			t.Errorf("outbound body = %q, want %q", body, payload)
		}
		return &http.Response{
			StatusCode:    http.StatusNoContent,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
			Request:       r,
		}, nil
	})
	handler := NewHandler(Options{
		Upstream:        parseURL(t, "https://example.com"),
		Transport:       transport,
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/", nil)
	req.Body = originalBody
	req.ContentLength = -1
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if !originalBody.closed {
		t.Fatal("original request body was not closed")
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("client read failed") }
func (failingBody) Close() error             { return nil }

func TestProxyRejectsUnreadableBody(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()

	handler := NewHandler(Options{
		Upstream:        parseURL(t, upstream.URL),
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/", nil)
	req.Body = failingBody{}
	req.ContentLength = -1
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := res.Body.String(), "{\"error\":\"invalid request body\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "upstream timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestProxyMapsTransportErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantBody: "{\"error\":\"gateway timeout\"}\n"},
		{name: "net timeout", err: timeoutError{}, wantStatus: http.StatusGatewayTimeout, wantBody: "{\"error\":\"gateway timeout\"}\n"},
		{name: "other", err: errors.New("connection refused"), wantStatus: http.StatusBadGateway, wantBody: "{\"error\":\"bad gateway\"}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(Options{
				Upstream: parseURL(t, "https://example.com"),
				Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return nil, tt.err
				}),
				MaxRequestBytes: 1024,
				Logger:          testLogger(),
			})
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://proxy.test/", strings.NewReader("{}")))
			if res.Code != tt.wantStatus || res.Body.String() != tt.wantBody {
				t.Fatalf("response = %d %q, want %d %q", res.Code, res.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestProxyTransportErrorLogOmitsRequestBody(t *testing.T) {
	const secret = "rpc-secret-body-do-not-log-7c76b"
	var logs bytes.Buffer
	handler := NewHandler(Options{
		Upstream: parseURL(t, "https://example.com"),
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
		MaxRequestBytes: 1024,
		Logger:          slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	payload := []byte(`{"jsonrpc":"2.0","secret":"` + secret + `"}`)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://proxy.test/", bytes.NewReader(payload)))

	if got, want := res.Code, http.StatusBadGateway; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if logs.Len() == 0 {
		t.Fatal("transport error produced no log entry")
	}
	if bytes.Contains(logs.Bytes(), []byte(secret)) {
		t.Fatal("transport-error log contains request body secret")
	}
}

func TestNewTransportSettings(t *testing.T) {
	transport := NewTransport(17 * time.Second)
	if transport.ResponseHeaderTimeout != 17*time.Second ||
		transport.TLSHandshakeTimeout != 10*time.Second ||
		transport.IdleConnTimeout != 90*time.Second ||
		transport.MaxIdleConns != 100 ||
		transport.MaxIdleConnsPerHost != 100 ||
		!transport.DisableCompression ||
		!transport.ForceAttemptHTTP2 {
		t.Fatalf("unexpected transport settings: %+v", transport)
	}
}

func TestProxyStreamsFlushedChunksThroughLoggingMiddleware(t *testing.T) {
	firstFlushed := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		close(firstFlushed)
		<-release
		_, _ = io.WriteString(w, "second\n")
	}))
	defer upstream.Close()

	proxyHandler := NewHandler(Options{
		Upstream:        parseURL(t, upstream.URL),
		Transport:       NewTransport(time.Second),
		MaxRequestBytes: 1024,
		Logger:          testLogger(),
	})
	server := httptest.NewServer(app.NewHandler(proxyHandler, testLogger(), buildinfo.Info{}))
	defer server.Close()

	var response *http.Response
	defer func() {
		releaseOnce.Do(func() { close(release) })
		if response != nil {
			_ = response.Body.Close()
		}
	}()

	type responseResult struct {
		response *http.Response
		err      error
	}
	responseResults := make(chan responseResult, 1)
	client := &http.Client{Timeout: 2 * time.Second}
	go func() {
		got, err := client.Get(server.URL + "/stream")
		responseResults <- responseResult{response: got, err: err}
	}()

	var result responseResult
	select {
	case result = <-responseResults:
	case <-time.After(time.Second):
		releaseOnce.Do(func() { close(release) })
		select {
		case result = <-responseResults:
		case <-time.After(2 * time.Second):
			t.Fatal("GET proxy did not stop after client timeout")
		}
		response = result.response
		t.Fatal("response headers were not delivered before upstream completed")
	}
	response = result.response
	if result.err != nil {
		t.Fatalf("GET proxy: %v", result.err)
	}
	select {
	case <-firstFlushed:
	case <-time.After(time.Second):
		t.Fatal("upstream did not flush first chunk")
	}

	reader := bufio.NewReader(response.Body)
	type lineResult struct {
		line string
		err  error
	}
	firstResult := make(chan lineResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		firstResult <- lineResult{line: line, err: err}
	}()

	select {
	case got := <-firstResult:
		if got.err != nil {
			t.Fatalf("read first chunk: %v", got.err)
		}
		if got.line != "first\n" {
			t.Fatalf("first chunk = %q, want %q", got.line, "first\n")
		}
	case <-time.After(time.Second):
		releaseOnce.Do(func() { close(release) })
		t.Fatal("first chunk was not delivered before upstream completed")
	}

	releaseOnce.Do(func() { close(release) })
	second, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read second chunk: %v", err)
	}
	if second != "second\n" {
		t.Fatalf("second chunk = %q, want %q", second, "second\n")
	}
}
