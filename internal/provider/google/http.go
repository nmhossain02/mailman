package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxResponseBody = 8 << 20

type apiClient struct {
	http *http.Client
	base string
}

func newAPIClient(client *http.Client, base string) apiClient {
	if client == nil {
		client = http.DefaultClient
	}
	return apiClient{http: client, base: base}
}

func (c apiClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) (*http.Response, error) {
	return c.doRequest(ctx, method, path, query, body, out, false)
}

func (c apiClient) doIdempotent(ctx context.Context, method, path string, query url.Values, body any, out any) (*http.Response, error) {
	return c.doRequest(ctx, method, path, query, body, out, true)
}

func (c apiClient) doRequest(ctx context.Context, method, path string, query url.Values, body any, out any, idempotent bool) (*http.Response, error) {
	var encoded io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		encoded = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) != 0 {
		u += "?" + query.Encode()
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 && body != nil {
			b, _ := json.Marshal(body)
			encoded = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, encoded)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil {
				err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out)
				if err == io.EOF {
					err = nil
				}
			}
			resp.Body.Close()
			return resp, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		last = &HTTPError{Status: resp.StatusCode, Body: string(data)}
		if attempt == 0 && retryable(method, resp.StatusCode, idempotent) {
			if delay := retryDelay(resp.Header.Get("Retry-After")); delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			continue
		}
		return resp, last
	}
	return nil, last
}

func retryable(method string, status int, explicitlyIdempotent bool) bool {
	if !explicitlyIdempotent && method != http.MethodGet && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete {
		return false
	}
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}

func retryDelay(value string) time.Duration {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 2 {
		return 0
	}
	return time.Duration(n) * time.Second
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("google API returned HTTP %d: %.512s", e.Status, e.Body)
}
