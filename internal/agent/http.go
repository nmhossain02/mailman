package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const MaxResponseBytes int64 = 2 << 20

type attemptObserverKey struct{}

func withAttemptObserver(ctx context.Context, observer func(int)) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observer)
}

// DoJSON performs the one bounded retry allowed for inference calls. Callers
// own request construction so credentials never enter trace data.
func DoJSON(ctx context.Context, client *http.Client, makeRequest func() (*http.Request, error)) (*http.Response, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	for attempt := 0; attempt < 2; attempt++ {
		if observer, ok := ctx.Value(attemptObserverKey{}).(func(int)); ok {
			observer(attempt + 1)
		}
		req, err := makeRequest()
		if err != nil {
			return nil, nil, err
		}
		resp, err := client.Do(req.WithContext(ctx))
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
			resp.Body.Close()
			if readErr != nil {
				return nil, nil, mapTransportError(ctx, readErr)
			}
			if int64(len(body)) > MaxResponseBytes {
				return resp, nil, &InferenceError{Kind: "invalid_output", SafeMessage: "inference response exceeded 2 MiB"}
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return resp, body, nil
			}
			mapped := mapStatus(resp.StatusCode)
			if attempt == 0 && mapped.Retriable && canRetry(ctx) {
				continue
			}
			return resp, nil, mapped
		}
		mapped := mapTransportError(ctx, err)
		var inferenceErr *InferenceError
		if attempt == 0 && errors.As(mapped, &inferenceErr) && inferenceErr.Retriable && canRetry(ctx) {
			continue
		}
		return nil, nil, mapped
	}
	panic("unreachable")
}

func canRetry(ctx context.Context) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > 0
}

func mapStatus(status int) *InferenceError {
	kind, retry := "invalid_request", false
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = "authentication"
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		kind, retry = "overloaded", true
	case http.StatusInternalServerError:
		kind = "unavailable"
	}
	return &InferenceError{Kind: kind, Retriable: retry, ProviderStatus: status, SafeMessage: fmt.Sprintf("inference provider returned HTTP %d", status)}
}

func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &InferenceError{Kind: "cancelled", SafeMessage: "inference request cancelled"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &InferenceError{Kind: "timeout", SafeMessage: "inference request timed out"}
	}
	var netErr net.Error
	retry := errors.As(err, &netErr)
	return &InferenceError{Kind: "unavailable", Retriable: retry, SafeMessage: "inference provider unavailable"}
}
