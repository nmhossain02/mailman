package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nmhossain02/mailman/internal/agent"
)

func TestInferExactRequestAndResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != false || body["model"] != "qwen" {
			t.Errorf("payload %#v", body)
		}
		if _, ok := body["format"].(map[string]any); !ok {
			t.Errorf("format %#v", body["format"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen","message":{"role":"assistant","content":"{\"ok\":true}"},"done":true,"done_reason":"stop","prompt_eval_count":4,"eval_count":2,"load_duration":1000000,"prompt_eval_duration":2000000,"eval_duration":3000000}`))
	}))
	defer server.Close()
	b := &Backend{BaseURL: server.URL, Client: server.Client()}
	result, err := b.Infer(context.Background(), agent.Request{Model: "qwen", Instructions: "safe", InputJSON: json.RawMessage(`{"x":1}`), OutputSchema: json.RawMessage(`{"type":"object"}`), MaxOutputTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.RawOutput) != `{"ok":true}` || *result.InputTokens != 4 || *result.GenerationMS != 3 {
		t.Fatalf("result %#v", result)
	}
}

func TestInferTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{}"},"done":false,"done_reason":"length"}`))
	}))
	defer server.Close()
	_, err := (&Backend{BaseURL: server.URL, Client: server.Client()}).Infer(context.Background(), agent.Request{OutputSchema: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestHealthRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen","digest":"sha256:abc"}]}`))
	}))
	defer server.Close()
	health := (&Backend{BaseURL: server.URL, Client: server.Client()}).Health(context.Background())
	if !health.Ready || health.ModelRevision != "sha256:abc" {
		t.Fatalf("health %#v", health)
	}
}
