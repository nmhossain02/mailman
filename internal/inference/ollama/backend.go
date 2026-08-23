package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/inference"
)

type Backend struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

func (b *Backend) ID() string { return "ollama" }

func (b *Backend) Health(ctx context.Context) inference.Health {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.BaseURL, "/")+"/api/tags", nil)
	if err != nil {
		return inference.Health{SafeMessage: "invalid Ollama URL"}
	}
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return inference.Health{SafeMessage: "Ollama unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return inference.Health{SafeMessage: fmt.Sprintf("Ollama returned HTTP %d", resp.StatusCode)}
	}
	var body struct {
		Models []struct{ Name, Digest string } `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return inference.Health{SafeMessage: "invalid Ollama health response"}
	}
	revision := ""
	for _, model := range body.Models {
		if b.Model == "" || model.Name == b.Model {
			revision = model.Digest
			break
		}
	}
	return inference.Health{Ready: true, ModelRevision: revision}
}

func (b *Backend) Infer(ctx context.Context, request inference.Request) (inference.ProviderResult, error) {
	payload := struct {
		Model    string          `json:"model"`
		Messages []message       `json:"messages"`
		Stream   bool            `json:"stream"`
		Format   json.RawMessage `json:"format"`
		Options  map[string]int  `json:"options,omitempty"`
	}{request.Model, []message{{"system", request.Instructions}, {"user", string(request.InputJSON)}}, false, request.OutputSchema, map[string]int{"num_predict": request.MaxOutputTokens}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return inference.ProviderResult{}, err
	}
	started := time.Now()
	_, data, err := inference.DoJSON(ctx, b.Client, func() (*http.Request, error) {
		req, requestErr := http.NewRequest(http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/api/chat", bytes.NewReader(encoded))
		if requestErr == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, requestErr
	})
	if err != nil {
		return inference.ProviderResult{}, err
	}
	var response struct {
		Model          string  `json:"model"`
		Message        message `json:"message"`
		Done           bool    `json:"done"`
		DoneReason     string  `json:"done_reason"`
		PromptCount    int64   `json:"prompt_eval_count"`
		OutputCount    int64   `json:"eval_count"`
		LoadDuration   int64   `json:"load_duration"`
		PromptDuration int64   `json:"prompt_eval_duration"`
		EvalDuration   int64   `json:"eval_duration"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return inference.ProviderResult{}, invalid("invalid Ollama response")
	}
	if !response.Done || response.DoneReason == "length" {
		return inference.ProviderResult{}, invalid("Ollama output was truncated")
	}
	if !json.Valid([]byte(response.Message.Content)) {
		return inference.ProviderResult{}, invalid("Ollama returned malformed JSON")
	}
	wall := time.Since(started).Milliseconds()
	load, prompt, generation := response.LoadDuration/int64(time.Millisecond), response.PromptDuration/int64(time.Millisecond), response.EvalDuration/int64(time.Millisecond)
	return inference.ProviderResult{RawOutput: json.RawMessage(response.Message.Content), ProviderMetadata: append(json.RawMessage(nil), data...), Model: response.Model, FinishReason: response.DoneReason, InputTokens: &response.PromptCount, OutputTokens: &response.OutputCount, LoadMS: &load, PromptMS: &prompt, GenerationMS: &generation, WallMS: &wall}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func invalid(message string) error {
	return &inference.InferenceError{Kind: "invalid_output", SafeMessage: message}
}

var _ inference.Backend = (*Backend)(nil)
