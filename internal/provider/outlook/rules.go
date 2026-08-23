package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/nabeel/mailman/internal/core"
	"github.com/nabeel/mailman/internal/provider"
)

type graphRule struct {
	ID, DisplayName                 string
	Sequence                        int
	IsEnabled, IsReadOnly, HasError bool
	Conditions, Exceptions          json.RawMessage
	Actions                         json.RawMessage
}

func (c *Client) ListRules(ctx context.Context) ([]provider.ProviderRule, error) {
	target := "/me/mailFolders/inbox/messageRules?$top=100"
	var out []provider.ProviderRule
	for target != "" {
		var page struct {
			Value []json.RawMessage `json:"value"`
			Next  string            `json:"@odata.nextLink"`
		}
		resp, err := c.request(ctx, http.MethodGet, target, nil, true)
		if err != nil {
			return nil, err
		}
		if err := decodeResponse(resp, &page); err != nil {
			return nil, err
		}
		for _, raw := range page.Value {
			var r graphRule
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			sequence := r.Sequence
			conditions, _ := importFilters(r.Conditions)
			exceptions, _ := importFilters(r.Exceptions)
			actions, _ := importActions(r.Actions)
			out = append(out, provider.ProviderRule{ID: r.ID, Name: r.DisplayName, Source: "provider", Enabled: r.IsEnabled, ReadOnly: r.IsReadOnly, Sequence: &sequence, Conditions: conditions, Exceptions: exceptions, Actions: actions, Raw: append(json.RawMessage(nil), raw...)})
		}
		target = page.Next
	}
	return out, nil
}

func importFilters(raw json.RawMessage) ([]core.Filter, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}
	var out []core.Filter
	supported := true
	for key, value := range obj {
		var values []string
		switch key {
		case "senderContains", "subjectContains", "bodyContains", "recipientContains":
			if json.Unmarshal(value, &values) != nil {
				supported = false
				continue
			}
			field := map[string]string{"senderContains": "from", "subjectContains": "subject", "bodyContains": "body", "recipientContains": "to"}[key]
			for _, v := range values {
				out = append(out, core.Filter{Field: field, Operator: "contains", Value: v})
			}
		case "isRead":
			var b bool
			if json.Unmarshal(value, &b) == nil {
				out = append(out, core.Filter{Field: "is_read", Operator: "eq", Value: strings.ToLower(strconvBool(b))})
			} else {
				supported = false
			}
		default:
			supported = false
		}
	}
	return out, supported
}
func strconvBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func importActions(raw json.RawMessage) ([]core.Action, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}
	var out []core.Action
	supported := true
	for key, value := range obj {
		switch key {
		case "markAsRead":
			var b bool
			if json.Unmarshal(value, &b) == nil && b {
				out = append(out, core.Action{Kind: "mark_read"})
			} else {
				supported = false
			}
		case "moveToFolder":
			var s string
			if json.Unmarshal(value, &s) == nil {
				out = append(out, core.Action{Kind: "archive", Argument: s})
			} else {
				supported = false
			}
		case "assignCategories":
			var values []string
			if json.Unmarshal(value, &values) == nil {
				for _, v := range values {
					out = append(out, core.Action{Kind: "add_label", Argument: v})
				}
			} else {
				supported = false
			}
		default:
			supported = false // forward/send/delete remain in Raw and cannot compile
		}
	}
	return out, supported
}

func compileRule(rule core.Rule) provider.RuleCompilation {
	draft := provider.ProviderRuleDraft{Name: rule.Name, Enabled: rule.Enabled, Sequence: rule.Sequence}
	remainder := rule
	remainder.Conditions, remainder.Exceptions, remainder.Actions = nil, nil, nil
	for _, f := range rule.Conditions {
		if supportedRuleFilter(f) {
			draft.Conditions = append(draft.Conditions, f)
		} else {
			remainder.Conditions = append(remainder.Conditions, f)
		}
	}
	for _, f := range rule.Exceptions {
		if supportedRuleFilter(f) {
			draft.Exceptions = append(draft.Exceptions, f)
		} else {
			remainder.Exceptions = append(remainder.Exceptions, f)
		}
	}
	for _, a := range rule.Actions {
		if a.Kind == "mark_read" || a.Kind == "archive" || a.Kind == "add_label" {
			draft.Actions = append(draft.Actions, a)
		} else {
			remainder.Actions = append(remainder.Actions, a)
		}
	}
	if len(remainder.Conditions) == 0 && len(remainder.Exceptions) == 0 && len(remainder.Actions) == 0 {
		return provider.RuleCompilation{Status: "supported", Draft: draft}
	}
	if len(draft.Actions) == 0 {
		return provider.RuleCompilation{Status: "unsupported", LocalRemainder: &remainder, Reason: "no provider-supported action"}
	}
	return provider.RuleCompilation{Status: "partial", Draft: draft, LocalRemainder: &remainder, Reason: "some conditions or actions require local evaluation"}
}

func supportedRuleFilter(f core.Filter) bool {
	return f.Operator == "contains" && (f.Field == "from" || f.Field == "to" || f.Field == "subject" || f.Field == "body")
}

func graphRuleDraft(d provider.ProviderRuleDraft) map[string]any {
	conditions := map[string][]string{}
	exceptions := map[string][]string{}
	actions := map[string]any{}
	addFilter := func(target map[string][]string, f core.Filter) {
		key := map[string]string{"from": "senderContains", "to": "recipientContains", "subject": "subjectContains", "body": "bodyContains"}[f.Field]
		if key != "" && f.Operator == "contains" {
			target[key] = append(target[key], f.Value)
		}
	}
	for _, f := range d.Conditions {
		addFilter(conditions, f)
	}
	for _, f := range d.Exceptions {
		addFilter(exceptions, f)
	}
	for _, a := range d.Actions {
		switch a.Kind {
		case "mark_read":
			actions["markAsRead"] = true
		case "archive":
			actions["moveToFolder"] = a.Argument
		case "add_label":
			actions["assignCategories"] = appendString(actions["assignCategories"], a.Argument)
		}
	}
	result := map[string]any{"displayName": d.Name, "isEnabled": d.Enabled, "conditions": conditions, "exceptions": exceptions, "actions": actions}
	if d.Sequence != nil {
		result["sequence"] = *d.Sequence
	}
	return result
}
func appendString(v any, s string) []string { values, _ := v.([]string); return append(values, s) }

func (c *Client) CreateRule(ctx context.Context, d provider.ProviderRuleDraft, key string) (provider.RuleReceipt, error) {
	resp, err := c.request(ctx, http.MethodPost, "/me/mailFolders/inbox/messageRules", graphRuleDraft(d), true)
	if err != nil {
		return provider.RuleReceipt{ExecutionKey: key, Status: "uncertain"}, nil
	}
	var raw json.RawMessage
	if err := decodeResponse(resp, &raw); err != nil {
		return provider.RuleReceipt{}, err
	}
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &v)
	return provider.RuleReceipt{ProviderID: v.ID, ExecutionKey: key, Status: "ok", Raw: raw}, nil
}
func (c *Client) UpdateRule(ctx context.Context, id string, d provider.ProviderRuleDraft, key string) (provider.RuleReceipt, error) {
	current, err := c.rule(ctx, id)
	if err != nil {
		return provider.RuleReceipt{}, err
	}
	if current.IsReadOnly {
		return provider.RuleReceipt{}, errors.New("cannot update read-only Outlook rule")
	}
	resp, err := c.request(ctx, http.MethodPatch, "/me/mailFolders/inbox/messageRules/"+url.PathEscape(id), graphRuleDraft(d), true)
	if err != nil {
		return provider.RuleReceipt{}, err
	}
	var raw json.RawMessage
	if err := decodeResponse(resp, &raw); err != nil {
		return provider.RuleReceipt{}, err
	}
	return provider.RuleReceipt{ProviderID: id, ExecutionKey: key, Status: "ok", Raw: raw}, nil
}
func (c *Client) DeleteRule(ctx context.Context, id string) error {
	current, err := c.rule(ctx, id)
	if err != nil {
		return err
	}
	if current.IsReadOnly {
		return errors.New("cannot delete read-only Outlook rule")
	}
	resp, err := c.request(ctx, http.MethodDelete, "/me/mailFolders/inbox/messageRules/"+url.PathEscape(id), nil, true)
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}
func (c *Client) rule(ctx context.Context, id string) (graphRule, error) {
	var v graphRule
	resp, err := c.request(ctx, http.MethodGet, "/me/mailFolders/inbox/messageRules/"+url.PathEscape(id), nil, true)
	if err != nil {
		return v, err
	}
	err = decodeResponse(resp, &v)
	return v, err
}
