package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nabeel/mailman/internal/inference"
	"github.com/nabeel/mailman/internal/inference/ollama"
	openaiadapter "github.com/nabeel/mailman/internal/inference/openai"
	"github.com/nabeel/mailman/internal/provider/google"
	"github.com/nabeel/mailman/internal/provider/outlook"
)

func bearerClient(token string) *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.Header = r.Header.Clone()
		copy.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(copy)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func gate(t *testing.T, name string) string {
	t.Helper()
	if os.Getenv("MAILMAN_SMOKE_"+name) != "1" {
		t.Skip("set MAILMAN_SMOKE_" + name + "=1 for this dedicated-account smoke")
	}
	token := os.Getenv("MAILMAN_SMOKE_" + name + "_TOKEN")
	if token == "" {
		t.Fatal("dedicated smoke token is required")
	}
	return token
}

func TestLiveGmailReadSmoke(t *testing.T) {
	token := gate(t, "GMAIL")
	g := google.NewGmail(bearerClient(token), "", "smoke")
	if _, err := g.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ListCollections(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestLiveTasksReadSmoke(t *testing.T) {
	token := gate(t, "TASKS")
	target := google.NewTasks(bearerClient(token), "", os.Getenv("MAILMAN_SMOKE_TASKS_LIST_ID"))
	if _, err := target.ListTaskLists(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestLiveCalendarReadSmoke(t *testing.T) {
	token := gate(t, "CALENDAR")
	target := google.NewCalendar(bearerClient(token), "", os.Getenv("MAILMAN_SMOKE_CALENDAR_ID"))
	if _, err := target.ListCalendars(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestLiveOutlookReadSmoke(t *testing.T) {
	token := gate(t, "OUTLOOK")
	client := outlook.NewClient("https://graph.microsoft.com/v1.0", nil, func(context.Context) (string, error) { return token, nil })
	if _, err := client.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListCollections(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveOllamaSchemaSmoke(t *testing.T) {
	if os.Getenv("MAILMAN_SMOKE_OLLAMA") != "1" {
		t.Skip("set MAILMAN_SMOKE_OLLAMA=1")
	}
	model := os.Getenv("MAILMAN_SMOKE_OLLAMA_MODEL")
	if model == "" {
		t.Fatal("MAILMAN_SMOKE_OLLAMA_MODEL required")
	}
	runTinySchema(t, &ollama.Backend{BaseURL: "http://127.0.0.1:11434", Model: model}, model)
}
func TestLiveOpenAISchemaSmoke(t *testing.T) {
	if os.Getenv("MAILMAN_SMOKE_OPENAI") != "1" {
		t.Skip("set MAILMAN_SMOKE_OPENAI=1; this incurs cost")
	}
	key := os.Getenv("OPENAI_API_KEY")
	model := os.Getenv("MAILMAN_SMOKE_OPENAI_MODEL")
	if key == "" || model == "" {
		t.Fatal("OPENAI_API_KEY and MAILMAN_SMOKE_OPENAI_MODEL required")
	}
	runTinySchema(t, &openaiadapter.Backend{BaseURL: "https://api.openai.com", APIKey: key}, model)
}
func runTinySchema(t *testing.T, b inference.Backend, model string) {
	t.Helper()
	task := inference.Task{Name: "smoke", Version: "1", PromptVersion: "1", SchemaVersion: "1", Instructions: "Return ok true.", Model: model, Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`), MaxOutputTokens: 20, Decode: func(raw json.RawMessage) (any, error) {
		var v struct {
			OK bool `json:"ok"`
		}
		return v, json.Unmarshal(raw, &v)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := inference.RunTask(ctx, b, task, json.RawMessage(`{}`), "smoke"); err != nil {
		t.Fatal(err)
	}
}
