package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nmhossain02/mailman/internal/adapters/keyring"
	"github.com/nmhossain02/mailman/internal/application/progress"
	"github.com/nmhossain02/mailman/internal/application/provider"
	core "github.com/nmhossain02/mailman/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "google", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGmailFullSyncPaginationAndFinalCheckpoint(t *testing.T) {
	var listCalls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me/messages":
			listCalls++
			if r.URL.Query().Get("pageToken") == "" {
				io.WriteString(w, `{"messages":[{"id":"m1"}],"nextPageToken":"next"}`)
			} else {
				io.WriteString(w, `{"messages":[]}`)
			}
		case r.URL.Path == "/users/me/messages/m1":
			w.Write(fixture(t, "gmail_message.json"))
		case r.URL.Path == "/users/me/profile":
			io.WriteString(w, `{"historyId":"99"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	g := NewGmail(s.Client(), s.URL, "a")
	var events []progress.Event
	ctx := progress.WithReporter(context.Background(), func(event progress.Event) { events = append(events, event) })
	p1, err := g.Sync(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Done || len(p1.Checkpoint) != 0 || len(p1.Continuation) == 0 {
		t.Fatalf("intermediate page leaked checkpoint: %+v", p1)
	}
	if len(events) != 1 || events[0].Stage != progress.StageMetadata || events[0].Current != 1 || events[0].Total != 1 {
		t.Fatalf("metadata progress = %+v", events)
	}
	p2, err := g.Sync(context.Background(), p1.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Done || len(p2.Checkpoint) == 0 || listCalls != 2 {
		t.Fatalf("final page = %+v, calls=%d", p2, listCalls)
	}
}

func TestGmailNoCheckpointWhenFinalProfileFails(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me/messages" {
			io.WriteString(w, `{"messages":[]}`)
			return
		}
		http.Error(w, "down", http.StatusBadRequest)
	}))
	defer s.Close()
	page, err := NewGmail(s.Client(), s.URL, "a").Sync(context.Background(), nil)
	if err == nil || len(page.Checkpoint) != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestGmailReportsContentProgress(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gmail_message.json"))
	}))
	defer s.Close()
	var events []progress.Event
	ctx := progress.WithReporter(context.Background(), func(event progress.Event) { events = append(events, event) })
	if _, err := NewGmail(s.Client(), s.URL, "a").FetchContent(ctx, []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Stage != progress.StageContent || events[0].Current != 1 || events[1].Current != 2 || events[1].Total != 2 {
		t.Fatalf("content progress = %+v", events)
	}
}

func TestGmailHistoryDeduplicatesAndExpires(t *testing.T) {
	expired := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/me/history" {
			if expired {
				http.Error(w, "expired", http.StatusNotFound)
			} else {
				w.Write(fixture(t, "gmail_history_duplicates.json"))
			}
			return
		}
		if r.URL.Path == "/users/me/messages/m1" {
			w.Write(fixture(t, "gmail_message.json"))
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	g := NewGmail(s.Client(), s.URL, "a")
	p, err := g.Sync(context.Background(), encodeCursor(gmailCursor{Mode: "history", HistoryID: "10"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Upserts) != 1 || len(p.DeletedIDs) != 1 || p.DeletedIDs[0] != "m2" {
		t.Fatalf("unexpected dedupe: %+v", p)
	}
	expired = true
	if _, err = g.Sync(context.Background(), encodeCursor(gmailCursor{Mode: "history", HistoryID: "10"})); err != ErrCursorExpired {
		t.Fatalf("got %v", err)
	}
}

func TestGmailBatchModifyChunksAtOneThousand(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/messages/batchModify" {
			http.NotFound(w, r)
			return
		}
		calls++
		var b struct {
			IDs []string `json:"ids"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if len(b.IDs) > 1000 {
			t.Errorf("chunk has %d IDs", len(b.IDs))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer s.Close()
	states := make([]provider.DesiredMailState, 2001)
	read := true
	for i := range states {
		states[i] = provider.DesiredMailState{ProviderMessageID: string(rune(i + 1)), ExecutionKey: string(rune(i + 1)), Read: &read}
	}
	got, err := NewGmail(s.Client(), s.URL, "a").Apply(context.Background(), states)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(got) != len(states) {
		t.Fatalf("calls=%d results=%d", calls, len(got))
	}
}

func TestGmailRuleDuplicateAvoidanceAndUnsafeCompilation(t *testing.T) {
	var posts int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(fixture(t, "gmail_filters.json"))
			return
		}
		posts++
		io.WriteString(w, `{"id":"new"}`)
	}))
	defer s.Close()
	g := NewGmail(s.Client(), s.URL, "a")
	d := provider.ProviderRuleDraft{Enabled: true, Conditions: []core.Filter{{Field: "sender", Operator: "contains", Value: "news@example.com"}}, Actions: []core.Action{{Kind: "archive"}}}
	receipt, err := g.CreateRule(context.Background(), d, "k")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProviderID != "existing" || posts != 0 {
		t.Fatalf("receipt=%+v posts=%d", receipt, posts)
	}
	if got := g.CompileRule(core.Rule{Enabled: true, Actions: []core.Action{{Kind: "forward"}}}); got.Status != "unsupported" {
		t.Fatalf("compile=%+v", got)
	}
}

func TestOAuthStatePKCEAndRefreshRetention(t *testing.T) {
	var forms []url.Values
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		forms = append(forms, r.Form)
		io.WriteString(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer s.Close()
	store := keyring.NewMemoryStore()
	o := NewOAuth(OAuthConfig{ClientID: "client", TokenURL: s.URL, AuthURL: s.URL + "/auth", TokenKey: "token"}, store, s.Client())
	session, err := o.Authorization([]string{ScopeTasks}, "http://127.0.0.1/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(session.URL)
	if u.Query().Get("code_challenge_method") != "S256" || strings.Contains(u.Query().Get("scope"), "mail.google.com") {
		t.Fatalf("bad auth URL %s", session.URL)
	}
	if _, err = o.Exchange(context.Background(), session, "wrong", "code"); err == nil {
		t.Fatal("state mismatch accepted")
	}
	old := Token{RefreshToken: "keep"}
	b, _ := json.Marshal(old)
	store.Set(context.Background(), "token", b)
	tok, err := o.Exchange(context.Background(), session, session.State, "code")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "keep" || forms[0].Get("code_verifier") != session.Verifier {
		t.Fatalf("token=%+v form=%v", tok, forms[0])
	}
}

func TestCalendarDeterministicRetryAndAttendeeOmission(t *testing.T) {
	var calls int
	var bodies [][]byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if calls == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"id":"ok"}`)
	}))
	defer s.Close()
	c := NewCalendar(s.Client(), s.URL, "primary")
	d := provider.EventDraft{Title: "Focus", Start: "2026-08-22T10:00:00-07:00", End: "2026-08-22T11:00:00-07:00", Timezone: "America/Los_Angeles"}
	r, err := c.EnsureEvent(context.Background(), d, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(r.ProviderID, EventID("same-key")) {
		t.Fatalf("calls=%d receipt=%+v", calls, r)
	}
	if string(bodies[0]) != string(bodies[1]) || strings.Contains(string(bodies[0]), "attendees") {
		t.Fatalf("non-deterministic or attendees included: %s / %s", bodies[0], bodies[1])
	}
}

func TestTasksDueDateAndUncertainReconciliationPaginates(t *testing.T) {
	var posted bool
	var listAfter int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["due"] != "2026-08-22T00:00:00Z" {
				t.Errorf("due=%v", b["due"])
			}
			http.Error(w, "lost", http.StatusInternalServerError)
			return
		}
		if !posted {
			io.WriteString(w, `{"items":[]}`)
			return
		}
		listAfter++
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"items":[],"nextPageToken":"two"}`)
		} else {
			io.WriteString(w, `{"items":[{"id":"found","notes":"mailman:exec"}]}`)
		}
	}))
	defer s.Close()
	target := NewTasks(s.Client(), s.URL, "list")
	r, err := target.EnsureTask(context.Background(), provider.TaskDraft{Title: "Do", DueDate: "2026-08-22"}, "exec")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "succeeded" || !strings.Contains(r.ProviderID, "found") || listAfter != 2 {
		t.Fatalf("receipt=%+v pages=%d", r, listAfter)
	}
}
