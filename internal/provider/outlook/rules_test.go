package outlook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nmhossain02/mailman/internal/core"
	"github.com/nmhossain02/mailman/internal/provider"
)

func TestCompileRulePartialForUnsupportedAction(t *testing.T) {
	c := &Client{}
	result := c.CompileRule(core.Rule{Name: "mixed", Enabled: true, Conditions: []core.Filter{{Field: "from", Operator: "contains", Value: "x"}}, Actions: []core.Action{{Kind: "mark_read"}, {Kind: "trash"}}})
	if result.Status != "partial" || result.LocalRemainder == nil {
		t.Fatalf("result=%+v", result)
	}
}
func TestReadOnlyRuleCannotBeMutated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r","displayName":"managed","isReadOnly":true}`))
	}))
	defer server.Close()
	c := testClient(server)
	if _, err := c.UpdateRule(context.Background(), "r", coreDraft(), "k"); err == nil {
		t.Fatal("updated read-only rule")
	}
	if err := c.DeleteRule(context.Background(), "r"); err == nil {
		t.Fatal("deleted read-only rule")
	}
}
func coreDraft() provider.ProviderRuleDraft {
	return provider.ProviderRuleDraft{Name: "x", Enabled: true, Actions: []core.Action{{Kind: "mark_read"}}}
}
