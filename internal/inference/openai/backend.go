package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/inference"
)

type Backend struct {
	BaseURL, APIKey string
	Client          *http.Client
}

func (b *Backend) ID() string { return "openai" }
func (b *Backend) Health(context.Context) inference.Health {
	if strings.TrimSpace(b.APIKey) == "" {
		return inference.Health{SafeMessage: "OpenAI API key is not configured"}
	}
	return inference.Health{Ready: true}
}

func (b *Backend) Infer(ctx context.Context, request inference.Request) (inference.ProviderResult, error) {
	var schema any
	if err := json.Unmarshal(request.OutputSchema, &schema); err != nil {
		return inference.ProviderResult{}, &inference.InferenceError{Kind: "invalid_request", SafeMessage: "invalid output schema"}
	}
	payload := map[string]any{
		"model": request.Model, "instructions": request.Instructions, "input": string(request.InputJSON),
		"max_output_tokens": request.MaxOutputTokens, "store": false,
		"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": request.TaskName, "schema": schema, "strict": true}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return inference.ProviderResult{}, err
	}
	started := time.Now()
	_, data, err := inference.DoJSON(ctx, b.Client, func() (*http.Request, error) {
		req, requestErr := http.NewRequest(http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/v1/responses", bytes.NewReader(encoded))
		if requestErr == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+b.APIKey)
		}
		return req, requestErr
	})
	if err != nil {
		return inference.ProviderResult{}, err
	}
	var response responseEnvelope
	if err := json.Unmarshal(data, &response); err != nil {
		return inference.ProviderResult{}, invalid("invalid OpenAI response")
	}
	if response.Status != "completed" {
		kind := "incomplete"
		if response.Status == "failed" {
			kind = "unavailable"
		}
		if response.IncompleteDetails.Reason == "max_output_tokens" {
			kind = "invalid_output"
		}
		return inference.ProviderResult{}, &inference.InferenceError{Kind: kind, SafeMessage: "OpenAI response was " + response.Status}
	}
	var outputText string
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			switch content.Type {
			case "refusal":
				return inference.ProviderResult{}, &inference.InferenceError{Kind: "refused", SafeMessage: "OpenAI refused the request"}
			case "output_text":
				if outputText != "" {
					return inference.ProviderResult{}, invalid("OpenAI returned multiple output texts")
				}
				outputText = content.Text
			}
		}
	}
	if outputText == "" || !json.Valid([]byte(outputText)) {
		return inference.ProviderResult{}, invalid("OpenAI returned no valid JSON output")
	}
	wall := time.Since(started).Milliseconds()
	return inference.ProviderResult{RawOutput: json.RawMessage(outputText), ProviderMetadata: append(json.RawMessage(nil), data...), ProviderResponseID: response.ID, Model: response.Model, FinishReason: response.Status, InputTokens: &response.Usage.InputTokens, CachedInputTokens: &response.Usage.InputTokensDetails.CachedTokens, OutputTokens: &response.Usage.OutputTokens, WallMS: &wall}, nil
}

type responseEnvelope struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func invalid(message string) error {
	return &inference.InferenceError{Kind: "invalid_output", SafeMessage: message}
}

var _ inference.Backend = (*Backend)(nil)
