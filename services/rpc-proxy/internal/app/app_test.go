package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	handler := NewHandler(downstream, discardLogger())

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

func TestHandlerProxiesOtherRequests(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := NewHandler(downstream, discardLogger())

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/"},
		{method: http.MethodPost, path: "/healthz"},
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
