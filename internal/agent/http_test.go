package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPStatusMappingAndRetry(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			_, _, err := DoJSON(context.Background(), server.Client(), func() (*http.Request, error) { return http.NewRequest(http.MethodGet, server.URL, nil) })
			var ie *InferenceError
			if !errors.As(err, &ie) || ie.ProviderStatus != status {
				t.Fatalf("err %#v", err)
			}
			want := int64(1)
			if status == 429 || status == 503 {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
		})
	}
}

func TestCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); cancel(); w.WriteHeader(503) }))
	defer server.Close()
	_, _, err := DoJSON(ctx, server.Client(), func() (*http.Request, error) { return http.NewRequest(http.MethodGet, server.URL, nil) })
	var ie *InferenceError
	if !errors.As(err, &ie) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(MaxResponseBytes)+1)))
	}))
	defer server.Close()
	_, _, err := DoJSON(context.Background(), server.Client(), func() (*http.Request, error) { return http.NewRequest(http.MethodGet, server.URL, nil) })
	if err == nil {
		t.Fatal("expected size error")
	}
}
