package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type Options struct {
	Upstream        *url.URL
	Transport       http.RoundTripper
	MaxRequestBytes int64
	Logger          *slog.Logger
}

func NewTransport(responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DisableCompression:    true,
	}
}

func NewHandler(opts Options) http.Handler {
	if opts.Upstream == nil {
		panic("proxy: nil upstream URL")
	}
	if opts.MaxRequestBytes <= 0 {
		panic("proxy: non-positive request byte limit")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Transport == nil {
		opts.Transport = NewTransport(30 * time.Second)
	}

	upstream := *opts.Upstream
	reverseProxy := &httputil.ReverseProxy{
		Transport: opts.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&upstream)
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.URL.ForceQuery = pr.In.URL.ForceQuery
			pr.SetXForwarded()
			pr.Out.Header.Del("Connection")
			pr.Out.Header.Del("Upgrade")
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			opts.Logger.Error("upstream transport failed", "method", r.Method, "path", r.URL.Path, "error", err)
			if isTimeout(err) {
				writeJSONError(w, http.StatusGatewayTimeout, "gateway timeout")
				return
			}
			writeJSONError(w, http.StatusBadGateway, "bad gateway")
		},
	}

	return limitRequestBody(opts.MaxRequestBytes, reverseProxy)
}

func limitRequestBody(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxBytes {
			_ = r.Body.Close()
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}

		body, tooLarge, err := readBounded(r.Body, maxBytes)
		_ = r.Body.Close()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if tooLarge {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

func readBounded(src io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := &io.LimitedReader{R: src, N: maxBytes}
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if limited.N > 0 {
		return body, false, nil
	}

	var sentinel [1]byte
	for {
		n, err := src.Read(sentinel[:])
		if err != nil && err != io.EOF {
			return nil, false, err
		}
		if n > 0 {
			return body, true, nil
		}
		if err == io.EOF {
			return body, false, nil
		}
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"error\":\""+message+"\"}\n")
}
