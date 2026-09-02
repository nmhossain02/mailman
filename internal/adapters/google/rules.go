package google

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/nmhossain02/mailman/internal/application/provider"
	core "github.com/nmhossain02/mailman/internal/domain"
)

type gmailFilter struct {
	ID       string `json:"id,omitempty"`
	Criteria struct {
		From           string `json:"from,omitempty"`
		To             string `json:"to,omitempty"`
		Subject        string `json:"subject,omitempty"`
		Query          string `json:"query,omitempty"`
		NegatedQuery   string `json:"negatedQuery,omitempty"`
		SizeComparison string `json:"sizeComparison,omitempty"`
		HasAttachment  bool   `json:"hasAttachment,omitempty"`
		Size           int64  `json:"size,omitempty"`
	} `json:"criteria"`
	Action struct {
		AddLabelIDs    []string `json:"addLabelIds,omitempty"`
		RemoveLabelIDs []string `json:"removeLabelIds,omitempty"`
		Forward        string   `json:"forward,omitempty"`
	} `json:"action"`
}

func (g *Gmail) ListRules(ctx context.Context) ([]provider.ProviderRule, error) {
	var list struct {
		Filter  []gmailFilter `json:"filter"`
		Filters []gmailFilter `json:"filters"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, "/users/me/settings/filters", nil, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Filters) == 0 {
		list.Filters = list.Filter
	}
	out := make([]provider.ProviderRule, 0, len(list.Filters))
	for _, f := range list.Filters {
		out = append(out, normalizeFilter(f))
	}
	return out, nil
}

func normalizeFilter(f gmailFilter) provider.ProviderRule {
	var filters []core.Filter
	add := func(field, value string) {
		if value != "" {
			filters = append(filters, core.Filter{Field: field, Operator: "contains", Value: value})
		}
	}
	add("sender", f.Criteria.From)
	add("recipient", f.Criteria.To)
	add("subject", f.Criteria.Subject)
	add("gmail_query", f.Criteria.Query)
	add("gmail_negated_query", f.Criteria.NegatedQuery)
	if f.Criteria.HasAttachment {
		filters = append(filters, core.Filter{Field: "has_attachment", Operator: "equals", Value: "true"})
	}
	if f.Criteria.Size > 0 {
		filters = append(filters, core.Filter{Field: "size", Operator: f.Criteria.SizeComparison, Value: fmt.Sprint(f.Criteria.Size)})
	}
	var actions []core.Action
	for _, id := range f.Action.AddLabelIDs {
		actions = append(actions, labelAction(id, true))
	}
	for _, id := range f.Action.RemoveLabelIDs {
		actions = append(actions, labelAction(id, false))
	}
	if f.Action.Forward != "" {
		actions = append(actions, core.Action{Kind: "provider_unsupported_forward", Argument: f.Action.Forward})
	}
	raw, _ := json.Marshal(f)
	return provider.ProviderRule{ID: f.ID, Name: "Gmail filter " + f.ID, Source: "provider", Enabled: true, ReadOnly: false, Conditions: filters, Actions: actions, Raw: raw}
}

func labelAction(id string, add bool) core.Action {
	if id == "INBOX" && !add {
		return core.Action{Kind: "archive"}
	}
	if id == "UNREAD" && !add {
		return core.Action{Kind: "mark_read"}
	}
	if id == "UNREAD" && add {
		return core.Action{Kind: "mark_unread"}
	}
	if id == "TRASH" && add {
		return core.Action{Kind: "trash"}
	}
	if add {
		return core.Action{Kind: "add_label", Argument: id}
	}
	return core.Action{Kind: "remove_label", Argument: id}
}

func (g *Gmail) CompileRule(rule core.Rule) provider.RuleCompilation {
	d := provider.ProviderRuleDraft{Name: rule.Name, Enabled: rule.Enabled, Conditions: append([]core.Filter(nil), rule.Conditions...), Exceptions: append([]core.Filter(nil), rule.Exceptions...), Actions: append([]core.Action(nil), rule.Actions...)}
	if len(rule.Exceptions) > 0 {
		remainder := rule
		return provider.RuleCompilation{Status: "partial", Draft: d, LocalRemainder: &remainder, Reason: "Gmail filters do not support structured exceptions"}
	}
	for _, a := range rule.Actions {
		if a.Kind == "forward" || a.Kind == "permanent_delete" || a.Kind == "send" {
			return provider.RuleCompilation{Status: "unsupported", Reason: "unsafe rule action is not supported"}
		}
	}
	if !rule.Enabled {
		return provider.RuleCompilation{Status: "unsupported", Reason: "Gmail filters cannot be disabled"}
	}
	return provider.RuleCompilation{Status: "supported", Draft: d}
}

func draftFilter(d provider.ProviderRuleDraft) (gmailFilter, error) {
	var f gmailFilter
	for _, c := range d.Conditions {
		switch c.Field {
		case "sender":
			f.Criteria.From = c.Value
		case "recipient":
			f.Criteria.To = c.Value
		case "subject":
			f.Criteria.Subject = c.Value
		case "gmail_query":
			f.Criteria.Query = c.Value
		case "gmail_negated_query":
			f.Criteria.NegatedQuery = c.Value
		case "has_attachment":
			f.Criteria.HasAttachment = c.Value == "true"
		default:
			return f, fmt.Errorf("unsupported Gmail filter field %q", c.Field)
		}
	}
	for _, a := range d.Actions {
		switch a.Kind {
		case "archive":
			f.Action.RemoveLabelIDs = append(f.Action.RemoveLabelIDs, "INBOX")
		case "mark_read":
			f.Action.RemoveLabelIDs = append(f.Action.RemoveLabelIDs, "UNREAD")
		case "mark_unread":
			f.Action.AddLabelIDs = append(f.Action.AddLabelIDs, "UNREAD")
		case "trash":
			f.Action.AddLabelIDs = append(f.Action.AddLabelIDs, "TRASH")
		case "add_label":
			f.Action.AddLabelIDs = append(f.Action.AddLabelIDs, a.Argument)
		case "remove_label":
			f.Action.RemoveLabelIDs = append(f.Action.RemoveLabelIDs, a.Argument)
		default:
			return f, fmt.Errorf("unsupported Gmail filter action %q", a.Kind)
		}
	}
	f.Action.AddLabelIDs = unique(f.Action.AddLabelIDs)
	f.Action.RemoveLabelIDs = unique(f.Action.RemoveLabelIDs)
	return f, nil
}
func canonicalFilter(f gmailFilter) string {
	f.ID = ""
	sort.Strings(f.Action.AddLabelIDs)
	sort.Strings(f.Action.RemoveLabelIDs)
	b, _ := json.Marshal(f)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (g *Gmail) CreateRule(ctx context.Context, d provider.ProviderRuleDraft, key string) (provider.RuleReceipt, error) {
	if !d.Enabled {
		return provider.RuleReceipt{ExecutionKey: key, Status: "failed"}, fmt.Errorf("Gmail filters cannot be disabled")
	}
	f, err := draftFilter(d)
	if err != nil {
		return provider.RuleReceipt{ExecutionKey: key, Status: "failed"}, err
	}
	var list struct {
		Filters []gmailFilter `json:"filter"`
	}
	if _, err = g.api.do(ctx, http.MethodGet, "/users/me/settings/filters", nil, nil, &list); err != nil {
		return provider.RuleReceipt{}, err
	}
	want := canonicalFilter(f)
	for _, existing := range list.Filters {
		if canonicalFilter(existing) == want {
			raw, _ := json.Marshal(existing)
			return provider.RuleReceipt{ProviderID: existing.ID, ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
		}
	}
	var created gmailFilter
	if _, err = g.api.do(ctx, http.MethodPost, "/users/me/settings/filters", nil, f, &created); err != nil {
		return provider.RuleReceipt{ExecutionKey: key, Status: "uncertain"}, err
	}
	raw, _ := json.Marshal(created)
	return provider.RuleReceipt{ProviderID: created.ID, ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
}

func (g *Gmail) UpdateRule(context.Context, string, provider.ProviderRuleDraft, string) (provider.RuleReceipt, error) {
	return provider.RuleReceipt{Status: "unsupported"}, fmt.Errorf("Gmail filters cannot be updated; replace them explicitly")
}
func (g *Gmail) DeleteRule(ctx context.Context, id string) error {
	_, err := g.api.do(ctx, http.MethodDelete, "/users/me/settings/filters/"+url.PathEscape(id), nil, nil, nil)
	return err
}

var _ = strings.Builder{}
