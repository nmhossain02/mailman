package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/core"
	"github.com/nmhossain02/mailman/internal/progress"
	"github.com/nmhossain02/mailman/internal/provider"
)

var ErrCursorExpired = errors.New("gmail history cursor expired")

type Gmail struct {
	api       apiClient
	accountID string
}

func NewGmail(client *http.Client, baseURL, accountID string) *Gmail {
	if baseURL == "" {
		baseURL = "https://gmail.googleapis.com/gmail/v1"
	}
	return &Gmail{api: newAPIClient(client, strings.TrimRight(baseURL, "/")), accountID: accountID}
}

func (g *Gmail) ID() string { return "gmail" }
func (g *Gmail) Capabilities(context.Context) (provider.Capabilities, error) {
	return provider.Capabilities{RuleCreate: true, RuleDelete: true, BatchApply: true, Restore: true}, nil
}

func (g *Gmail) Account(ctx context.Context) (provider.ProviderAccount, error) {
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	_, err := g.api.do(ctx, http.MethodGet, "/users/me/profile", nil, nil, &p)
	return provider.ProviderAccount{ID: g.accountID, Address: p.EmailAddress}, err
}

func (g *Gmail) ListCollections(ctx context.Context) ([]provider.ProviderCollection, error) {
	var r struct {
		Labels []struct{ ID, Name, Type string } `json:"labels"`
	}
	_, err := g.api.do(ctx, http.MethodGet, "/users/me/labels", nil, nil, &r)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ProviderCollection, 0, len(r.Labels))
	for _, l := range r.Labels {
		out = append(out, provider.ProviderCollection{ID: l.ID, Name: l.Name, Kind: "label"})
	}
	return out, nil
}

type gmailCursor struct{ HistoryID, PageToken, Mode string }

func decodeCursor(raw provider.OpaqueCursor) (gmailCursor, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return gmailCursor{Mode: "full"}, nil
	}
	var c gmailCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("decode gmail cursor: %w", err)
	}
	if c.Mode == "" {
		c.Mode = "history"
	}
	return c, nil
}
func encodeCursor(c gmailCursor) provider.OpaqueCursor { b, _ := json.Marshal(c); return b }

func (g *Gmail) Sync(ctx context.Context, raw provider.OpaqueCursor) (provider.SyncPage, error) {
	c, err := decodeCursor(raw)
	if err != nil {
		return provider.SyncPage{}, err
	}
	if c.Mode == "full" {
		return g.fullSync(ctx, c)
	}
	return g.historySync(ctx, c)
}

func (g *Gmail) fullSync(ctx context.Context, c gmailCursor) (provider.SyncPage, error) {
	q := url.Values{"maxResults": {"500"}}
	if c.PageToken != "" {
		q.Set("pageToken", c.PageToken)
	}
	var r struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, "/users/me/messages", q, nil, &r); err != nil {
		return provider.SyncPage{}, err
	}
	messageIDs := make([]string, len(r.Messages))
	for i := range r.Messages {
		messageIDs[i] = r.Messages[i].ID
	}
	upserts, err := g.fetchMetadata(ctx, messageIDs)
	if err != nil {
		return provider.SyncPage{}, err
	}
	if r.NextPageToken != "" {
		return provider.SyncPage{Upserts: upserts, Continuation: encodeCursor(gmailCursor{Mode: "full", PageToken: r.NextPageToken}), Done: false}, nil
	}
	var p struct {
		HistoryID string `json:"historyId"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, "/users/me/profile", nil, nil, &p); err != nil {
		return provider.SyncPage{}, err
	}
	return provider.SyncPage{Upserts: upserts, Checkpoint: encodeCursor(gmailCursor{Mode: "history", HistoryID: p.HistoryID}), Done: true}, nil
}

func (g *Gmail) historySync(ctx context.Context, c gmailCursor) (provider.SyncPage, error) {
	q := url.Values{"startHistoryId": {c.HistoryID}, "historyTypes": {"messageAdded", "messageDeleted", "labelAdded", "labelRemoved"}, "maxResults": {"500"}}
	if c.PageToken != "" {
		q.Set("pageToken", c.PageToken)
	}
	var r struct {
		History []struct {
			ID       string `json:"id"`
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			MessagesAdded []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"messagesAdded"`
			MessagesDeleted []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"messagesDeleted"`
			LabelsAdded []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"labelsAdded"`
			LabelsRemoved []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"labelsRemoved"`
		} `json:"history"`
		NextPageToken, HistoryID string
	}
	_, err := g.api.do(ctx, http.MethodGet, "/users/me/history", q, nil, &r)
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return provider.SyncPage{}, ErrCursorExpired
	}
	if err != nil {
		return provider.SyncPage{}, err
	}
	changed, deleted := map[string]bool{}, map[string]bool{}
	for _, h := range r.History {
		for _, m := range h.Messages {
			changed[m.ID] = true
		}
		for _, m := range h.MessagesAdded {
			changed[m.Message.ID] = true
		}
		for _, m := range h.LabelsAdded {
			changed[m.Message.ID] = true
		}
		for _, m := range h.LabelsRemoved {
			changed[m.Message.ID] = true
		}
		for _, m := range h.MessagesDeleted {
			deleted[m.Message.ID] = true
			delete(changed, m.Message.ID)
		}
	}
	changeIDs, deletedIDs := keys(changed), keys(deleted)
	upserts, err := g.fetchMetadata(ctx, changeIDs)
	if err != nil {
		return provider.SyncPage{}, err
	}
	page := provider.SyncPage{Upserts: upserts, DeletedIDs: deletedIDs}
	if r.NextPageToken != "" {
		page.Continuation = encodeCursor(gmailCursor{Mode: "history", HistoryID: c.HistoryID, PageToken: r.NextPageToken})
		return page, nil
	}
	if r.HistoryID == "" {
		r.HistoryID = c.HistoryID
	}
	page.Checkpoint = encodeCursor(gmailCursor{Mode: "history", HistoryID: r.HistoryID})
	page.Done = true
	return page, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type gmailMessage struct {
	ID, ThreadID, HistoryID, InternalDate string
	LabelIDs                              []string
	Payload                               gmailPart
	Raw                                   json.RawMessage
}
type gmailPart struct {
	MimeType string                         `json:"mimeType"`
	Headers  []struct{ Name, Value string } `json:"headers"`
	Body     struct{ Data string }          `json:"body"`
	Parts    []gmailPart                    `json:"parts"`
}

func (g *Gmail) fetchMetadata(ctx context.Context, ids []string) ([]provider.ProviderMessage, error) {
	out := make([]provider.ProviderMessage, 0, len(ids))
	for _, id := range ids {
		q := url.Values{"format": {"metadata"}, "metadataHeaders": {"Subject", "From", "To", "Cc", "Message-ID"}}
		var m gmailMessage
		if _, err := g.api.do(ctx, http.MethodGet, "/users/me/messages/"+url.PathEscape(id), q, nil, &m); err != nil {
			return nil, err
		}
		m.Raw, _ = json.Marshal(m)
		out = append(out, g.toProvider(m, false))
		progress.Report(ctx, progress.Event{Stage: progress.StageMetadata, Current: len(out), Total: len(ids)})
	}
	return out, nil
}

func (g *Gmail) toProvider(m gmailMessage, loaded bool) provider.ProviderMessage {
	h := headers(m.Payload.Headers)
	ms, _ := strconv.ParseInt(m.InternalDate, 10, 64)
	return provider.ProviderMessage{StableID: m.ID, ConversationKey: m.ThreadID, Revision: m.HistoryID, InternetMessageID: h["message-id"], Subject: h["subject"], Sender: h["from"], Recipients: splitAddresses(h["to"] + "," + h["cc"]), TagIDs: m.LabelIDs, ReceivedAt: time.UnixMilli(ms).UTC(), Read: !contains(m.LabelIDs, "UNREAD"), FolderID: folder(m.LabelIDs), ContentLoaded: loaded, Raw: m.Raw}
}

func headers(in []struct{ Name, Value string }) map[string]string {
	out := map[string]string{}
	for _, h := range in {
		out[strings.ToLower(h.Name)] = h.Value
	}
	return out
}
func splitAddresses(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func folder(labels []string) string {
	for _, v := range []string{"INBOX", "SENT", "TRASH", "SPAM", "DRAFT"} {
		if contains(labels, v) {
			return v
		}
	}
	return ""
}

func (g *Gmail) FetchContent(ctx context.Context, ids []string) ([]provider.ProviderContent, error) {
	out := make([]provider.ProviderContent, 0, len(ids))
	for _, id := range ids {
		var m gmailMessage
		if _, err := g.api.do(ctx, http.MethodGet, "/users/me/messages/"+url.PathEscape(id), url.Values{"format": {"full"}}, nil, &m); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(m)
		out = append(out, provider.ProviderContent{MessageID: id, PlainText: partText(m.Payload), Raw: raw})
		progress.Report(ctx, progress.Event{Stage: progress.StageContent, Current: len(out), Total: len(ids)})
	}
	return out, nil
}
func partText(p gmailPart) string {
	if strings.HasPrefix(p.MimeType, "text/plain") {
		b, _ := base64.RawURLEncoding.DecodeString(p.Body.Data)
		return string(b)
	}
	for _, c := range p.Parts {
		if s := partText(c); s != "" {
			return s
		}
	}
	return ""
}

func (g *Gmail) Apply(ctx context.Context, desired []provider.DesiredMailState) ([]provider.OperationResult, error) {
	results := make([]provider.OperationResult, 0, len(desired))
	type modification struct {
		state       provider.DesiredMailState
		add, remove []string
	}
	groups := map[string][]modification{}
	for _, s := range desired {
		add, remove := append([]string(nil), s.EnsureTags...), append([]string(nil), s.RemoveTags...)
		if s.Read != nil {
			if *s.Read {
				remove = append(remove, "UNREAD")
			} else {
				add = append(add, "UNREAD")
			}
		}
		switch s.Disposition {
		case "archive":
			remove = append(remove, "INBOX")
		case "restore", "trash":
			verb := s.Disposition
			if verb == "restore" {
				verb = "untrash"
			}
			// Apply orthogonal read/label changes before changing disposition.
			var err error
			if len(add) != 0 || len(remove) != 0 {
				_, err = g.api.doIdempotent(ctx, http.MethodPost, "/users/me/messages/batchModify", nil, map[string]any{"ids": []string{s.ProviderMessageID}, "addLabelIds": unique(add), "removeLabelIds": unique(remove)}, nil)
			}
			if err == nil {
				_, err = g.api.doIdempotent(ctx, http.MethodPost, "/users/me/messages/"+url.PathEscape(s.ProviderMessageID)+"/"+verb, nil, map[string]any{}, nil)
			}
			results = append(results, opResult(s, err))
			continue
		case "":
		case "delete":
			results = append(results, provider.OperationResult{ExecutionKey: s.ExecutionKey, Status: "failed", ErrKind: "unsupported", SafeMessage: "permanent delete is not supported"})
			continue
		default:
			results = append(results, provider.OperationResult{ExecutionKey: s.ExecutionKey, Status: "failed", ErrKind: "invalid", SafeMessage: "unsupported disposition"})
			continue
		}
		add, remove = unique(add), unique(remove)
		keyBytes, _ := json.Marshal(struct{ Add, Remove []string }{add, remove})
		groups[string(keyBytes)] = append(groups[string(keyBytes)], modification{state: s, add: add, remove: remove})
	}
	for _, group := range groups {
		for start := 0; start < len(group); start += 1000 {
			end := start + 1000
			if end > len(group) {
				end = len(group)
			}
			chunk := group[start:end]
			messageIDs := make([]string, len(chunk))
			for i := range chunk {
				messageIDs[i] = chunk[i].state.ProviderMessageID
			}
			_, err := g.api.doIdempotent(ctx, http.MethodPost, "/users/me/messages/batchModify", nil, map[string]any{"ids": messageIDs, "addLabelIds": chunk[0].add, "removeLabelIds": chunk[0].remove}, nil)
			for _, item := range chunk {
				results = append(results, opResult(item.state, err))
			}
		}
	}
	return results, nil
}
func opResult(s provider.DesiredMailState, err error) provider.OperationResult {
	r := provider.OperationResult{ExecutionKey: s.ExecutionKey, RemoteID: s.ProviderMessageID, Status: "succeeded"}
	if err != nil {
		r.Status = "failed"
		r.ErrKind = "provider"
		r.SafeMessage = err.Error()
	}
	return r
}
func unique(in []string) []string {
	m := map[string]bool{}
	var o []string
	for _, v := range in {
		if v != "" && !m[v] {
			m[v] = true
			o = append(o, v)
		}
	}
	sort.Strings(o)
	return o
}

var _ provider.MailProvider = (*Gmail)(nil)

// Keep the compiler honest about the frozen core type used by rule compilation.
var _ = core.Rule{}
