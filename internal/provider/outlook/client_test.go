package outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nabeel/mailman/internal/provider"
)

func testClient(server *httptest.Server) *Client {
	return NewClient(server.URL, server.Client(), func(context.Context) (string, error) { return "token", nil })
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "outlook", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFolderTraversalAndDeltaCheckpoint(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Prefer"); got != immutablePreference {
			t.Errorf("%s missing immutable preference, got %q", r.URL.Path, got)
		}
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/mailFolders":
			_, _ = w.Write([]byte(`{"value":[{"id":"inbox","displayName":"Inbox","childFolderCount":1}]}`))
		case "/me/mailFolders/inbox/childFolders":
			_, _ = w.Write([]byte(`{"value":[{"id":"child","displayName":"Project","parentFolderId":"inbox"}]}`))
		case "/me/mailFolders/child/messages/delta":
			_, _ = w.Write([]byte(`{"value":[{"id":"m1","conversationId":"c1","conversationIndex":"AAE=","parentFolderId":"child","subject":"Hi","isRead":false,"categories":["Keep"]}],"@odata.nextLink":"` + serverURL(r) + `/saved/next"}`))
		case "/saved/next":
			_, _ = w.Write([]byte(`{"value":[],"@odata.deltaLink":"` + serverURL(r) + `/saved/delta-child"}`))
		case "/me/mailFolders/inbox/messages/delta":
			_, _ = w.Write([]byte(`{"value":[],"@odata.deltaLink":"` + serverURL(r) + `/saved/delta-inbox"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := testClient(server)
	first, err := c.Sync(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Upserts) != 1 || first.Done || len(first.Checkpoint) != 0 || len(first.Continuation) == 0 {
		t.Fatalf("bad first page: %+v", first)
	}
	second, err := c.Sync(context.Background(), first.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	if second.Done || len(second.Checkpoint) != 0 {
		t.Fatal("checkpoint promoted before all folders completed")
	}
	third, err := c.Sync(context.Background(), second.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Done || len(third.Checkpoint) == 0 {
		t.Fatalf("final page did not promote checkpoint: %+v", third)
	}
	var cursor syncCursor
	if err := json.Unmarshal(third.Checkpoint, &cursor); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cursor.Delta["child"], "/saved/delta-child") || !strings.HasSuffix(cursor.Delta["inbox"], "/saved/delta-inbox") {
		t.Fatalf("delta links not saved verbatim: %+v", cursor.Delta)
	}
}
func serverURL(r *http.Request) string { return "http://" + r.Host }

func TestDeltaNormalizesRemovalAndUpsertForMove(t *testing.T) {
	body := fixture(t, "delta_move.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Prefer") != immutablePreference {
			t.Error("missing immutable ID header")
		}
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "BASE_URL", serverURL(r))))
	}))
	defer server.Close()
	c := testClient(server)
	cur, _ := json.Marshal(syncCursor{Folders: []string{"inbox"}, Delta: map[string]string{}})
	page, err := c.Sync(context.Background(), cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.DeletedIDs) != 1 || page.DeletedIDs[0] != "stable" || len(page.Upserts) != 1 || page.Upserts[0].StableID != "stable" || page.Upserts[0].FolderID != "archive" {
		t.Fatalf("move not delete+upsert: %+v", page)
	}
}

func TestApplyPreservesCategoriesAndOrdersPatchBeforeMove(t *testing.T) {
	var batch struct {
		Requests []batchRequest `json:"requests"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/outlook/masterCategories" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"value":[{"id":"keep","displayName":"Add","color":"preset0"}]}`))
		case r.Method == http.MethodGet:
			if r.Header.Get("Prefer") != immutablePreference {
				t.Error("GET missing immutable")
			}
			_, _ = w.Write([]byte(`{"categories":["Keep","Remove"]}`))
		case r.URL.Path == "/$batch":
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"responses":[{"id":"p0","status":200,"body":{}},{"id":"m0","status":201,"body":{"id":"new-id"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := testClient(server)
	read := true
	results, err := c.Apply(context.Background(), []provider.DesiredMailState{{ProviderMessageID: "m", ExecutionKey: "k", Read: &read, EnsureTags: []string{"Add"}, RemoveTags: []string{"Remove"}, Disposition: "archive"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("results=%+v", results)
	}
	if len(batch.Requests) != 2 || len(batch.Requests[1].DependsOn) != 1 || batch.Requests[1].DependsOn[0] != batch.Requests[0].ID {
		t.Fatalf("compound action unordered: %+v", batch.Requests)
	}
	for _, request := range batch.Requests {
		if request.Headers["Prefer"] != immutablePreference {
			t.Errorf("batch member %s missing immutable", request.ID)
		}
	}
	body := batch.Requests[0].Body.(map[string]any)
	categories := body["categories"].([]any)
	joined := ""
	for _, v := range categories {
		joined += v.(string) + ","
	}
	if !strings.Contains(joined, "Keep") || !strings.Contains(joined, "Add") || strings.Contains(joined, "Remove") {
		t.Fatalf("categories=%v", categories)
	}
}

func TestBatchInspectsMixedResultsAndRetriesOnlyThrottledItem(t *testing.T) {
	retried := 0
	mixed := fixture(t, "batch_mixed.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/$batch" {
			_, _ = w.Write([]byte(mixed))
			return
		}
		if r.URL.Path == "/me/messages/two" {
			retried++
			if r.Header.Get("Prefer") != immutablePreference {
				t.Error("retry missing immutable preference")
			}
			_, _ = w.Write([]byte(`{"id":"two"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	c := testClient(server)
	responses, err := c.doBatch(context.Background(), []batchRequest{{ID: "one", Method: "PATCH", URL: "/me/messages/one", Headers: map[string]string{"Prefer": immutablePreference}, Body: map[string]bool{"isRead": true}}, {ID: "two", Method: "PATCH", URL: "/me/messages/two", Headers: map[string]string{"Prefer": immutablePreference}, Body: map[string]bool{"isRead": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if retried != 1 {
		t.Fatalf("retry count=%d", retried)
	}
	if responses[0].Status != 400 || responses[1].Status != 200 {
		t.Fatalf("responses=%+v", responses)
	}
}

func TestBatchRejectsMoreThanTwentyOperations(t *testing.T) {
	c := &Client{}
	requests := make([]batchRequest, 21)
	if _, err := c.doBatch(context.Background(), requests); err == nil {
		t.Fatal("accepted oversized Graph batch")
	}
}
