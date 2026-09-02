// Package automation evaluates deterministic local rules and resolves competing
// recommendations without invoking a model or provider.
package automation

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	core "github.com/nmhossain02/mailman/internal/domain"
)

type Context struct {
	Message       core.Message
	Kind          string
	AwaitingMe    bool
	HasDeadline   bool
	Starred       bool
	Now           time.Time
	RetentionTags []string
	Stale         bool
	SafeToArchive bool
}

// DeriveContext computes conservative state from the current conversation.
// "awaiting me" means the newest message came from someone else. Automatic
// archival is safe only for old, read conversations with no open reply/deadline.
func DeriveContext(messages []core.Message, self string, now time.Time, hasDeadline bool) Context {
	ctx := Context{Now: now, HasDeadline: hasDeadline}
	if len(messages) == 0 {
		return ctx
	}
	latest := messages[0]
	for _, m := range messages[1:] {
		if m.ReceivedAt.After(latest.ReceivedAt) {
			latest = m
		}
	}
	ctx.Message = latest
	ctx.AwaitingMe = !strings.EqualFold(strings.TrimSpace(latest.Sender), strings.TrimSpace(self))
	ctx.Stale = now.Sub(latest.ReceivedAt) >= 30*24*time.Hour
	ctx.SafeToArchive = ctx.Stale && latest.Read && !ctx.AwaitingMe && !hasDeadline
	return ctx
}

type Candidate struct {
	TargetType, TargetID, ExpectedRevision string
	Action                                 core.Action
	Source                                 string // safety|user|native|inferred|default
	RuleID                                 string
}

var precedence = map[string]int{"default": 1, "inferred": 2, "native": 3, "user": 4, "safety": 5}

// Evaluate returns compound effects for every matching local rule. Conditions
// are ANDed and any matching exception suppresses the entire rule.
func Evaluate(rules []core.Rule, ctx Context) []Candidate {
	var out []Candidate
	for _, rule := range rules {
		if !rule.Enabled || (rule.AccountID != "" && rule.AccountID != ctx.Message.AccountID) || !all(rule.Conditions, ctx) || any(rule.Exceptions, ctx) {
			continue
		}
		for _, action := range rule.Actions {
			out = append(out, Candidate{TargetType: "message", TargetID: ctx.Message.ID, ExpectedRevision: ctx.Message.Revision, Action: action, Source: "user", RuleID: rule.ID})
		}
	}
	return out
}

// Resolve applies fixed precedence and safety retention. At equal precedence,
// contradictory actions are dropped so a human sees neither as an arbitrary
// winner; independent actions remain compound.
func Resolve(in []Candidate, retentionTags []string) []Candidate {
	byKey := make(map[string]Candidate)
	conflicted := make(map[string]bool)
	for _, c := range in {
		if (c.Action.Kind == "trash" || c.Action.Kind == "archive") && len(retentionTags) > 0 {
			continue
		}
		key := c.TargetType + "\x00" + c.TargetID + "\x00" + family(c.Action)
		old, ok := byKey[key]
		if !ok || precedence[c.Source] > precedence[old.Source] {
			byKey[key], conflicted[key] = c, false
			continue
		}
		if precedence[c.Source] == precedence[old.Source] && (c.Action.Kind != old.Action.Kind || c.Action.Argument != old.Action.Argument) {
			conflicted[key] = true
		}
	}
	out := make([]Candidate, 0, len(byKey))
	for key, c := range byKey {
		if !conflicted[key] {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		return strings.Compare(a.TargetID+"\x00"+a.Action.Kind+"\x00"+a.Action.Argument, b.TargetID+"\x00"+b.Action.Kind+"\x00"+b.Action.Argument)
	})
	return out
}

func all(filters []core.Filter, ctx Context) bool {
	for _, f := range filters {
		if !match(f, ctx) {
			return false
		}
	}
	return true
}
func any(filters []core.Filter, ctx Context) bool {
	for _, f := range filters {
		if match(f, ctx) {
			return true
		}
	}
	return false
}

func match(f core.Filter, ctx Context) bool {
	actual := ""
	switch f.Field {
	case "kind":
		actual = ctx.Kind
	case "read":
		actual = strconv.FormatBool(ctx.Message.Read)
	case "starred":
		actual = strconv.FormatBool(ctx.Starred)
	case "sender":
		actual = ctx.Message.Sender
	case "sender_domain":
		if at := strings.LastIndex(ctx.Message.Sender, "@"); at >= 0 {
			actual = ctx.Message.Sender[at+1:]
		}
	case "label":
		return compareList(ctx.Message.TagIDs, f.Operator, f.Value)
	case "folder":
		actual = ctx.Message.FolderID
	case "awaiting":
		actual = strconv.FormatBool(ctx.AwaitingMe)
	case "has_deadline":
		actual = strconv.FormatBool(ctx.HasDeadline)
	case "subject":
		actual = ctx.Message.Subject
	case "body":
		actual = ctx.Message.NormalizedBody
	case "age_days":
		actual = strconv.FormatInt(int64(ctx.Now.Sub(ctx.Message.ReceivedAt).Hours()/24), 10)
	case "received_at":
		actual = ctx.Message.ReceivedAt.UTC().Format(time.RFC3339)
	default:
		return false
	}
	return compare(actual, f.Operator, f.Value)
}

func compareList(values []string, op, expected string) bool {
	found := slices.Contains(values, expected)
	if op == "eq" || op == "contains" || op == "in" {
		return found
	}
	if op == "ne" {
		return !found
	}
	return false
}
func compare(actual, op, expected string) bool {
	switch op {
	case "eq":
		return strings.EqualFold(actual, expected)
	case "ne":
		return !strings.EqualFold(actual, expected)
	case "contains":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(expected))
	case "in":
		for _, v := range strings.Split(expected, ",") {
			if strings.EqualFold(actual, strings.TrimSpace(v)) {
				return true
			}
		}
		return false
	case "lt", "lte", "gt", "gte":
		a, ae := strconv.ParseFloat(actual, 64)
		b, be := strconv.ParseFloat(expected, 64)
		if ae != nil || be != nil {
			return false
		}
		switch op {
		case "lt":
			return a < b
		case "lte":
			return a <= b
		case "gt":
			return a > b
		default:
			return a >= b
		}
	default:
		panic(fmt.Sprintf("validated filter has unknown operator %q", op))
	}
}
func family(action core.Action) string {
	switch action.Kind {
	case "archive", "trash":
		return "disposition"
	case "mark_read", "mark_unread":
		return "read"
	case "add_label", "remove_label":
		return "label:" + action.Argument
	default:
		return action.Kind
	}
}
