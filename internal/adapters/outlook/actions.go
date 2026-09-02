package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/application/provider"
)

type batchRequest struct {
	ID        string            `json:"id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      any               `json:"body,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
}
type batchResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type MasterCategory struct {
	ID, Name, Color string
}

func (c *Client) ListMasterCategories(ctx context.Context) ([]MasterCategory, error) {
	var page struct {
		Value []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			Color       string `json:"color"`
		} `json:"value"`
	}
	resp, err := c.request(ctx, http.MethodGet, "/me/outlook/masterCategories", nil, true)
	if err != nil {
		return nil, err
	}
	if err := decodeResponse(resp, &page); err != nil {
		return nil, err
	}
	out := make([]MasterCategory, 0, len(page.Value))
	for _, item := range page.Value {
		out = append(out, MasterCategory{ID: item.ID, Name: item.DisplayName, Color: item.Color})
	}
	return out, nil
}

func (c *Client) ensureMasterCategories(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	existing, err := c.ListMasterCategories(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, category := range existing {
		have[strings.ToLower(category.Name)] = true
	}
	for _, name := range names {
		if have[strings.ToLower(name)] {
			continue
		}
		resp, err := c.request(ctx, http.MethodPost, "/me/outlook/masterCategories", map[string]string{"displayName": name, "color": "preset0"}, true)
		if err != nil {
			return err
		}
		if err := decodeResponse(resp, nil); err != nil {
			return err
		}
		have[strings.ToLower(name)] = true
	}
	return nil
}

func (c *Client) categories(ctx context.Context, id string) ([]string, error) {
	var v struct {
		Categories []string `json:"categories"`
	}
	resp, err := c.request(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=categories", nil, true)
	if err != nil {
		return nil, err
	}
	if err := decodeResponse(resp, &v); err != nil {
		return nil, err
	}
	return v.Categories, nil
}

func mergedCategories(current, ensure, remove []string) []string {
	set := make(map[string]bool, len(current)+len(ensure))
	for _, v := range current {
		set[v] = true
	}
	for _, v := range ensure {
		set[v] = true
	}
	for _, v := range remove {
		delete(set, v)
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sortStrings(out)
	return out
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func (c *Client) Apply(ctx context.Context, states []provider.DesiredMailState) ([]provider.OperationResult, error) {
	results := make([]provider.OperationResult, 0, len(states))
	for start := 0; start < len(states); start += 10 { // up to PATCH+move per state => <=20
		end := start + 10
		if end > len(states) {
			end = len(states)
		}
		requests := make([]batchRequest, 0, (end-start)*2)
		owners := make(map[string]int)
		for i, desired := range states[start:end] {
			resultIndex := len(results)
			results = append(results, provider.OperationResult{ExecutionKey: desired.ExecutionKey, Status: "pending"})
			patch := make(map[string]any)
			if err := c.ensureMasterCategories(ctx, desired.EnsureTags); err != nil {
				results[resultIndex].Status = "failed"
				results[resultIndex].SafeMessage = err.Error()
				continue
			}
			if desired.Read != nil {
				patch["isRead"] = *desired.Read
			}
			if len(desired.EnsureTags) > 0 || len(desired.RemoveTags) > 0 {
				current, err := c.categories(ctx, desired.ProviderMessageID)
				if err != nil {
					results[resultIndex].Status = "failed"
					results[resultIndex].SafeMessage = err.Error()
					continue
				}
				patch["categories"] = mergedCategories(current, desired.EnsureTags, desired.RemoveTags)
			}
			patchID := "p" + strconv.Itoa(start+i)
			headers := map[string]string{"Prefer": immutablePreference, "Content-Type": "application/json"}
			if desired.ExpectedRevision != "" {
				headers["If-Match"] = desired.ExpectedRevision
			}
			if len(patch) > 0 {
				requests = append(requests, batchRequest{ID: patchID, Method: "PATCH", URL: "/me/messages/" + url.PathEscape(desired.ProviderMessageID), Headers: headers, Body: patch})
				owners[patchID] = resultIndex
			}
			destination := desired.DestinationCollectionID
			if destination == "" && desired.Disposition == "archive" {
				destination = "archive"
			}
			if destination == "" && desired.Disposition == "trash" {
				destination = "deleteditems"
			}
			if destination != "" {
				moveID := "m" + strconv.Itoa(start+i)
				request := batchRequest{ID: moveID, Method: "POST", URL: "/me/messages/" + url.PathEscape(desired.ProviderMessageID) + "/move", Headers: headers, Body: map[string]string{"destinationId": destination}}
				if len(patch) > 0 {
					request.DependsOn = []string{patchID}
				}
				requests = append(requests, request)
				owners[moveID] = resultIndex
			}
			if len(patch) == 0 && destination == "" {
				results[resultIndex].Status = "ok"
			}
		}
		if len(requests) == 0 {
			continue
		}
		responses, err := c.doBatch(ctx, requests)
		if err != nil {
			for i := range results {
				if results[i].Status == "pending" {
					results[i].Status = "uncertain"
					results[i].ErrKind = "unavailable"
					results[i].SafeMessage = "Graph batch outcome is uncertain; reconcile before retrying"
				}
			}
			return results, nil
		}
		for _, response := range responses {
			idx, ok := owners[response.ID]
			if !ok {
				continue
			}
			if response.Status >= 200 && response.Status < 300 {
				if results[idx].Status != "failed" {
					results[idx].Status = "ok"
				}
				var moved struct {
					ID        string `json:"id"`
					ChangeKey string `json:"changeKey"`
				}
				_ = json.Unmarshal(response.Body, &moved)
				if moved.ID != "" {
					results[idx].RemoteID = moved.ID
					results[idx].NewRevision = moved.ChangeKey
				}
			} else {
				results[idx].Status = "failed"
				results[idx].ErrKind = graphErrorKind(response.Status)
				results[idx].SafeMessage = fmt.Sprintf("Graph operation failed with status %d", response.Status)
			}
		}
	}
	return results, nil
}

func graphErrorKind(status int) string {
	if status == 429 {
		return "rate_limited"
	}
	if status >= 500 {
		return "unavailable"
	}
	if status == 401 || status == 403 {
		return "authentication"
	}
	return "invalid_request"
}

func (c *Client) doBatch(ctx context.Context, requests []batchRequest) ([]batchResponse, error) {
	if len(requests) > 20 {
		return nil, fmt.Errorf("Graph batch has %d operations; maximum is 20", len(requests))
	}
	var envelope struct {
		Responses []batchResponse `json:"responses"`
	}
	resp, err := c.request(ctx, http.MethodPost, "/$batch", map[string]any{"requests": requests}, false)
	if err != nil {
		return nil, err
	}
	if err := decodeResponse(resp, &envelope); err != nil {
		return nil, err
	}
	for i := range envelope.Responses {
		item := &envelope.Responses[i]
		if item.Status != 429 {
			continue
		}
		req := findBatchRequest(requests, item.ID)
		if req == nil {
			continue
		}
		delay := retryDelay(item.Headers)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		retryResp, err := c.request(ctx, req.Method, req.URL, req.Body, true)
		if err != nil {
			continue
		}
		body, _ := ioReadBounded(retryResp)
		item.Status = retryResp.StatusCode
		item.Body = body
	}
	return envelope.Responses, nil
}

func ioReadBounded(resp *http.Response) (json.RawMessage, error) {
	defer resp.Body.Close()
	var raw json.RawMessage
	err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(&raw)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return nil, err
	}
	return raw, nil
}
func findBatchRequest(reqs []batchRequest, id string) *batchRequest {
	for i := range reqs {
		if reqs[i].ID == id {
			return &reqs[i]
		}
	}
	return nil
}
func retryDelay(headers map[string]string) time.Duration {
	for key, value := range headers {
		if strings.EqualFold(key, "Retry-After") {
			if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 0
}
