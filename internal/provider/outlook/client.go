package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/core"
	"github.com/nmhossain02/mailman/internal/provider"
)

const immutablePreference = `IdType="ImmutableId"`

type Client struct {
	baseURL string
	http    *http.Client
	token   func(context.Context) (string, error)
}

func NewClient(baseURL string, httpClient *http.Client, token func(context.Context) (string, error)) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, token: token}
}

func (c *Client) ID() string { return "outlook" }

func (c *Client) Capabilities(context.Context) (provider.Capabilities, error) {
	return provider.Capabilities{RuleCreate: true, RuleUpdate: true, RuleDisable: true, RuleDelete: true, RuleOrder: true, RuleStopProcessing: true, BatchApply: true, Restore: true}, nil
}

func (c *Client) request(ctx context.Context, method, target string, body any, immutable bool) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = c.baseURL + "/" + strings.TrimLeft(target, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if immutable {
		req.Header.Set("Prefer", immutablePreference)
	}
	if c.token != nil {
		token, err := c.token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.http.Do(req)
}

func decodeResponse(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 2<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(limited, 8<<10))
		return fmt.Errorf("graph status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	return json.NewDecoder(limited).Decode(dst)
}

func (c *Client) Account(ctx context.Context) (provider.ProviderAccount, error) {
	var v struct{ ID, Mail, UserPrincipalName, DisplayName string }
	resp, err := c.request(ctx, http.MethodGet, "/me?$select=id,mail,userPrincipalName,displayName", nil, true)
	if err != nil {
		return provider.ProviderAccount{}, err
	}
	if err := decodeResponse(resp, &v); err != nil {
		return provider.ProviderAccount{}, err
	}
	address := v.Mail
	if address == "" {
		address = v.UserPrincipalName
	}
	return provider.ProviderAccount{ID: v.ID, Address: address, DisplayName: v.DisplayName}, nil
}

type graphFolder struct {
	ID, DisplayName, ParentFolderID string
	ChildFolderCount                int
}

func (c *Client) ListCollections(ctx context.Context) ([]provider.ProviderCollection, error) {
	queue := []string{"/me/mailFolders?$top=100&includeHiddenFolders=false"}
	var out []provider.ProviderCollection
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		for target != "" {
			var page struct {
				Value []graphFolder `json:"value"`
				Next  string        `json:"@odata.nextLink"`
			}
			resp, err := c.request(ctx, http.MethodGet, target, nil, true)
			if err != nil {
				return nil, err
			}
			if err := decodeResponse(resp, &page); err != nil {
				return nil, err
			}
			for _, f := range page.Value {
				out = append(out, provider.ProviderCollection{ID: f.ID, Name: f.DisplayName, Kind: "folder", ParentID: f.ParentFolderID})
				if f.ChildFolderCount > 0 {
					queue = append(queue, "/me/mailFolders/"+url.PathEscape(f.ID)+"/childFolders?$top=100&includeHiddenFolders=false")
				}
			}
			target = page.Next
		}
	}
	return out, nil
}

type syncCursor struct {
	Folders      []string          `json:"folders"`
	Delta        map[string]string `json:"delta,omitempty"`
	Index        int               `json:"index,omitempty"`
	Next         string            `json:"next,omitempty"`
	PendingDelta string            `json:"pending_delta,omitempty"`
}

type graphMessage struct {
	ID, ChangeKey, ConversationID, ConversationIndex, InternetMessageID, Subject string
	ParentFolderID                                                               string    `json:"parentFolderId"`
	ReceivedDateTime                                                             time.Time `json:"receivedDateTime"`
	IsRead                                                                       bool      `json:"isRead"`
	Categories                                                                   []string  `json:"categories"`
	From                                                                         struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	ToRecipients []struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"toRecipients"`
	Removed json.RawMessage `json:"@removed"`
}

func parseCursor(raw provider.OpaqueCursor) (syncCursor, error) {
	cur := syncCursor{Delta: make(map[string]string)}
	if len(raw) == 0 {
		return cur, nil
	}
	if err := json.Unmarshal(raw, &cur); err != nil {
		return cur, fmt.Errorf("outlook cursor: %w", err)
	}
	if cur.Delta == nil {
		cur.Delta = make(map[string]string)
	}
	return cur, nil
}

func encodeCursor(cur syncCursor) provider.OpaqueCursor { b, _ := json.Marshal(cur); return b }

func (c *Client) Sync(ctx context.Context, raw provider.OpaqueCursor) (provider.SyncPage, error) {
	cur, err := parseCursor(raw)
	if err != nil {
		return provider.SyncPage{}, err
	}
	if len(cur.Folders) == 0 {
		collections, err := c.ListCollections(ctx)
		if err != nil {
			return provider.SyncPage{}, err
		}
		for _, f := range collections {
			cur.Folders = append(cur.Folders, f.ID)
		}
		sort.Strings(cur.Folders)
	}
	if cur.Index >= len(cur.Folders) {
		return provider.SyncPage{Checkpoint: encodeCursor(cur), Done: true}, nil
	}
	folder := cur.Folders[cur.Index]
	target := cur.Next
	if target == "" {
		target = cur.Delta[folder]
	}
	if target == "" {
		target = "/me/mailFolders/" + url.PathEscape(folder) + "/messages/delta?$select=id,changeKey,conversationId,conversationIndex,internetMessageId,subject,from,toRecipients,receivedDateTime,isRead,parentFolderId,categories&$top=100"
	}
	var page struct {
		Value []graphMessage `json:"value"`
		Next  string         `json:"@odata.nextLink"`
		Delta string         `json:"@odata.deltaLink"`
	}
	resp, err := c.request(ctx, http.MethodGet, target, nil, true)
	if err != nil {
		return provider.SyncPage{}, err
	}
	if err := decodeResponse(resp, &page); err != nil {
		return provider.SyncPage{}, err
	}
	result := provider.SyncPage{}
	for _, m := range page.Value {
		if len(m.Removed) > 0 {
			result.DeletedIDs = append(result.DeletedIDs, m.ID)
			continue
		}
		recipients := make([]string, 0, len(m.ToRecipients))
		for _, r := range m.ToRecipients {
			recipients = append(recipients, r.EmailAddress.Address)
		}
		rawMessage, _ := json.Marshal(m)
		result.Upserts = append(result.Upserts, provider.ProviderMessage{StableID: m.ID, ConversationKey: m.ConversationID, Revision: m.ChangeKey, FolderID: m.ParentFolderID, InternetMessageID: m.InternetMessageID, Subject: m.Subject, Sender: m.From.EmailAddress.Address, Recipients: recipients, TagIDs: append([]string(nil), m.Categories...), ReceivedAt: m.ReceivedDateTime, Read: m.IsRead, Raw: rawMessage})
	}
	if page.Next != "" {
		cur.Next = page.Next
		result.Continuation = encodeCursor(cur)
		return result, nil
	}
	if page.Delta == "" {
		return provider.SyncPage{}, errors.New("graph delta page missing nextLink and deltaLink")
	}
	cur.Delta[folder] = page.Delta // saved verbatim
	cur.Next = ""
	cur.Index++
	if cur.Index < len(cur.Folders) {
		result.Continuation = encodeCursor(cur)
		return result, nil
	}
	cur.Index = 0
	result.Checkpoint = encodeCursor(cur)
	result.Done = true
	return result, nil
}

func (c *Client) FetchContent(ctx context.Context, ids []string) ([]provider.ProviderContent, error) {
	out := make([]provider.ProviderContent, 0, len(ids))
	for _, id := range ids {
		var v struct {
			ID          string
			Body        struct{ Content, ContentType string } `json:"body"`
			BodyPreview string                                `json:"bodyPreview"`
		}
		resp, err := c.request(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?$select=id,body,bodyPreview", nil, true)
		if err != nil {
			return nil, err
		}
		if err := decodeResponse(resp, &v); err != nil {
			return nil, err
		}
		rawBody, _ := json.Marshal(v.Body)
		text := v.Body.Content
		if strings.EqualFold(v.Body.ContentType, "html") {
			text = v.BodyPreview
		}
		out = append(out, provider.ProviderContent{MessageID: v.ID, PlainText: text, Raw: rawBody})
	}
	return out, nil
}

var _ provider.MailProvider = (*Client)(nil)

// CompileRule reports whether the canonical rule can be represented without loss.
func (c *Client) CompileRule(rule core.Rule) provider.RuleCompilation { return compileRule(rule) }
