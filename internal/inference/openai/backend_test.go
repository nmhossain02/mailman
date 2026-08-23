package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nmhossain02/mailman/internal/inference"
)

func TestInferExactRequestAndResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request %s auth %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["store"] != false || body["max_output_tokens"] != float64(50) {
			t.Errorf("payload %#v", body)
		}
		if _, ok := body["tools"]; ok {
			t.Error("tools must be absent")
		}
		format := body["text"].(map[string]any)["format"].(map[string]any)
		if format["strict"] != true || format["type"] != "json_schema" {
			t.Errorf("format %#v", format)
		}
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}],"usage":{"input_tokens":9,"input_tokens_details":{"cached_tokens":3},"output_tokens":4}}`))
	}))
	defer server.Close()
	result, err := (&Backend{BaseURL: server.URL, APIKey: "secret", Client: server.Client()}).Infer(context.Background(), inference.Request{TaskName: "kind", Model: "gpt-test", InputJSON: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object"}`), MaxOutputTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderResponseID != "resp_1" || *result.InputTokens != 9 || *result.CachedInputTokens != 3 {
		t.Fatalf("result %#v", result)
	}
}

func TestTerminalStates(t *testing.T) {
	cases := map[string]string{
		"refusal":    `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`,
		"failed":     `{"status":"failed"}`,
		"incomplete": `{"status":"incomplete","incomplete_details":{"reason":"other"}}`,
		"truncated":  `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`,
		"multiple":   `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]},{"type":"message","content":[{"type":"output_text","text":"{}"}]}]}`,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			_, err := (&Backend{BaseURL: server.URL, APIKey: "x", Client: server.Client()}).Infer(context.Background(), inference.Request{OutputSchema: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
