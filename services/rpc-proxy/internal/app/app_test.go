package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"twth/rpcproxy/internal/buildinfo"
	"twth/rpcproxy/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHandlerServesHealth(t *testing.T) {
	downstreamCalls := 0
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls++
		w.WriteHeader(http.StatusTeapot)
	})
	handler := NewHandler(downstream, discardLogger(), buildinfo.Info{})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/healthz", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := res.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if downstreamCalls != 0 {
		t.Fatalf("downstream calls = %d, want 0", downstreamCalls)
	}
}

func TestHandlerServesVersion(t *testing.T) {
	downstreamCalls := 0
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls++
		w.WriteHeader(http.StatusTeapot)
	})
	info := buildinfo.Info{
		Version: "1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	handler := NewHandler(downstream, discardLogger(), info)

	req := httptest.NewRequest(http.MethodGet, "http://proxy.test/version", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got, want := res.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := res.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Body.String(), "{\"version\":\"1.2.3\",\"commit\":\"0123456789abcdef0123456789abcdef01234567\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if downstreamCalls != 0 {
		t.Fatalf("downstream calls = %d, want 0", downstreamCalls)
	}
}

func TestHandlerProxiesOtherRequests(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := NewHandler(downstream, discardLogger(), buildinfo.Info{})

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/"},
		{method: http.MethodPost, path: "/healthz"},
		{method: http.MethodPost, path: "/version"},
		{method: http.MethodGet, path: "/other"},
	} {
		req := httptest.NewRequest(tc.method, "http://proxy.test"+tc.path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if got, want := res.Code, http.StatusAccepted; got != want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, got, want)
		}
	}
}

func TestHandlerLogsRequestMetadataWithoutBody(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "abc")
	})
	handler := NewHandler(downstream, logger, buildinfo.Info{})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/rpc?secret=do-not-log", strings.NewReader("sensitive-body"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	entry := logs.String()
	for _, want := range []string{`"method":"POST"`, `"path":"/rpc"`, `"status":201`, `"bytes":3`} {
		if !strings.Contains(entry, want) {
			t.Errorf("log entry %q does not contain %q", entry, want)
		}
	}
	for _, forbidden := range []string{"sensitive-body", "secret=do-not-log"} {
		if strings.Contains(entry, forbidden) {
			t.Errorf("log entry contains forbidden value %q: %s", forbidden, entry)
		}
	}
}

type statusRecordingWriter struct {
	header   http.Header
	statuses []int
}

func (w *statusRecordingWriter) Header() http.Header {
	return w.header
}

func (w *statusRecordingWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *statusRecordingWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
}

func TestResponseRecorderDoesNotLatchInformationalStatus(t *testing.T) {
	underlying := &statusRecordingWriter{header: make(http.Header)}
	recorder := &responseRecorder{ResponseWriter: underlying}

	recorder.WriteHeader(http.StatusEarlyHints)
	recorder.WriteHeader(http.StatusNoContent)

	if got, want := recorder.status, http.StatusNoContent; got != want {
		t.Fatalf("recorded status = %d, want %d", got, want)
	}
	if got, want := fmt.Sprint(underlying.statuses), "[103 204]"; got != want {
		t.Fatalf("delegated statuses = %s, want %s", got, want)
	}
}

func TestResponseRecorderTreatsSwitchingProtocolsAsFinal(t *testing.T) {
	underlying := &statusRecordingWriter{header: make(http.Header)}
	recorder := &responseRecorder{ResponseWriter: underlying}

	recorder.WriteHeader(http.StatusSwitchingProtocols)
	recorder.WriteHeader(http.StatusNoContent)

	if got, want := recorder.status, http.StatusSwitchingProtocols; got != want {
		t.Fatalf("recorded status = %d, want %d", got, want)
	}
	if got, want := fmt.Sprint(underlying.statuses), "[101]"; got != want {
		t.Fatalf("delegated statuses = %s, want %s", got, want)
	}
}

type readerFromResponseWriter struct {
	header          http.Header
	status          int
	body            bytes.Buffer
	readerFromCalls int
}

func (w *readerFromResponseWriter) Header() http.Header {
	return w.header
}

func (w *readerFromResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func (w *readerFromResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *readerFromResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readerFromCalls++
	return io.Copy(&w.body, src)
}

type basicResponseWriter struct {
	header     http.Header
	status     int
	body       bytes.Buffer
	writeCalls int
}

func (w *basicResponseWriter) Header() http.Header {
	return w.header
}

func (w *basicResponseWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	return w.body.Write(p)
}

func (w *basicResponseWriter) WriteHeader(status int) {
	w.status = status
}

func TestResponseRecorderReadFrom(t *testing.T) {
	const payload = "streamed response"

	t.Run("delegates to ReaderFrom", func(t *testing.T) {
		underlying := &readerFromResponseWriter{header: make(http.Header)}
		recorder := &responseRecorder{ResponseWriter: underlying}

		n, err := recorder.ReadFrom(strings.NewReader(payload))

		if err != nil {
			t.Fatalf("ReadFrom() error = %v", err)
		}
		if got, want := n, int64(len(payload)); got != want {
			t.Errorf("ReadFrom() bytes = %d, want %d", got, want)
		}
		if got, want := recorder.bytes, int64(len(payload)); got != want {
			t.Errorf("recorded bytes = %d, want %d", got, want)
		}
		if underlying.readerFromCalls != 1 {
			t.Errorf("underlying ReadFrom calls = %d, want 1", underlying.readerFromCalls)
		}
		if got, want := underlying.status, http.StatusOK; got != want {
			t.Errorf("underlying status = %d, want %d", got, want)
		}
		if got := underlying.body.String(); got != payload {
			t.Errorf("underlying body = %q, want %q", got, payload)
		}
	})

	t.Run("falls back to copy", func(t *testing.T) {
		underlying := &basicResponseWriter{header: make(http.Header)}
		recorder := &responseRecorder{ResponseWriter: underlying}

		n, err := recorder.ReadFrom(strings.NewReader(payload))

		if err != nil {
			t.Fatalf("ReadFrom() error = %v", err)
		}
		if got, want := n, int64(len(payload)); got != want {
			t.Errorf("ReadFrom() bytes = %d, want %d", got, want)
		}
		if got, want := recorder.bytes, int64(len(payload)); got != want {
			t.Errorf("recorded bytes = %d, want %d", got, want)
		}
		if underlying.writeCalls == 0 {
			t.Error("fallback did not write to underlying response writer")
		}
		if got, want := underlying.status, http.StatusOK; got != want {
			t.Errorf("underlying status = %d, want %d", got, want)
		}
		if got := underlying.body.String(); got != payload {
			t.Errorf("underlying body = %q, want %q", got, payload)
		}
	})
}

func TestRunGracefullyDrainsInFlightRequest(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	listenAddr := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseRequest)
		}
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	})
	cfg := config.Config{
		ListenAddr:      listenAddr,
		ShutdownTimeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg, handler, discardLogger())
	}()

	requestDone := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		deadline := time.Now().Add(2 * time.Second)
		for {
			res, err := client.Get("http://" + listenAddr + "/slow")
			if err != nil {
				if time.Now().After(deadline) {
					requestDone <- fmt.Errorf("GET /slow: %w", err)
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			_, copyErr := io.Copy(io.Discard, res.Body)
			closeErr := res.Body.Close()
			if copyErr != nil {
				requestDone <- fmt.Errorf("read response: %w", copyErr)
				return
			}
			if closeErr != nil {
				requestDone <- fmt.Errorf("close response: %w", closeErr)
				return
			}
			if res.StatusCode != http.StatusNoContent {
				requestDone <- fmt.Errorf("status = %d, want %d", res.StatusCode, http.StatusNoContent)
				return
			}
			requestDone <- nil
			return
		}
	}()

	select {
	case <-requestStarted:
	case err := <-requestDone:
		t.Fatalf("request completed before handler started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for request to start")
	}

	cancel()
	select {
	case err := <-runDone:
		t.Fatalf("Run() returned before in-flight request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRequest)
	released = true
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for request to complete")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Run() to stop")
	}
}
