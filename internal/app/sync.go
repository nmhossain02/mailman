package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nabeel/mailman/internal/core"
	"github.com/nabeel/mailman/internal/provider"
)

// SyncRepository deliberately makes page persistence one operation. A SQLite
// adapter must commit upserts/deletes and a final checkpoint in one transaction.
type SyncRepository interface {
	Cursor(context.Context, string, string) (string, bool, error)
	Conversation(context.Context, string) (core.Conversation, error)
	ConversationMessages(context.Context, string) ([]core.Message, error)
	CommitSyncPage(context.Context, string, string, []core.Message, []core.Conversation, []string, string, time.Time) error
	ReplaceProviderRules(context.Context, string, string, []core.Rule) error
}

type ConversationProcessor interface {
	ProcessConversation(context.Context, core.Conversation, []core.Message, core.RoutePolicy) error
}

type SyncService struct {
	Store     SyncRepository
	Processor ConversationProcessor
	Now       func() time.Time
}

type SyncResult struct{ Pages, ChangedMessages, ChangedConversations int }

func (s SyncService) Sync(ctx context.Context, accountID, scope string, mail provider.MailProvider, route core.RoutePolicy) (SyncResult, error) {
	if s.Store == nil || mail == nil {
		return SyncResult{}, fmt.Errorf("sync service is not configured")
	}
	checkpoint, _, err := s.Store.Cursor(ctx, accountID, scope)
	if err != nil {
		return SyncResult{}, fmt.Errorf("read checkpoint: %w", err)
	}
	cursor := provider.OpaqueCursor(checkpoint)
	changed := make(map[string][]core.Message)
	conversations := make(map[string]core.Conversation)
	result := SyncResult{}
	for {
		page, err := mail.Sync(ctx, cursor)
		if err != nil {
			return result, fmt.Errorf("sync page: %w", err)
		}
		messages, convs := normalizePage(accountID, page.Upserts)
		for i := range convs {
			if old, e := s.Store.Conversation(ctx, convs[i].ID); e == nil && old.LastMessageAt.After(convs[i].LastMessageAt) {
				convs[i].LastMessageAt = old.LastMessageAt
				convs[i].Subject = old.Subject
			}
		}
		deleted := make([]string, len(page.DeletedIDs))
		for i, id := range page.DeletedIDs {
			deleted[i] = accountID + ":" + id
		}
		bodyIDs := make([]string, 0)
		for _, m := range page.Upserts {
			if !m.ContentLoaded {
				bodyIDs = append(bodyIDs, m.StableID)
			}
		}
		if len(bodyIDs) > 0 {
			contents, fetchErr := mail.FetchContent(ctx, bodyIDs)
			if fetchErr != nil {
				return result, fmt.Errorf("fetch selected content: %w", fetchErr)
			}
			bodyByID := make(map[string]string, len(contents))
			for _, c := range contents {
				bodyByID[c.MessageID] = c.PlainText
			}
			for i := range messages {
				if b, ok := bodyByID[messages[i].ProviderID]; ok {
					messages[i].NormalizedBody = b
				}
			}
		}
		promote := ""
		if page.Done {
			promote = string(page.Checkpoint)
		}
		now := time.Now().UTC()
		if s.Now != nil {
			now = s.Now().UTC()
		}
		if err = s.Store.CommitSyncPage(ctx, accountID, scope, messages, convs, deleted, promote, now); err != nil {
			return result, fmt.Errorf("commit sync page: %w", err)
		}
		result.Pages++
		result.ChangedMessages += len(messages)
		for _, m := range messages {
			changed[m.ConversationID] = append(changed[m.ConversationID], m)
		}
		for _, c := range convs {
			conversations[c.ID] = c
		}
		if page.Done {
			break
		}
		if len(page.Continuation) == 0 {
			return result, fmt.Errorf("provider returned unfinished page without continuation")
		}
		cursor = page.Continuation
	}
	rules, err := mail.ListRules(ctx)
	if err != nil {
		return result, fmt.Errorf("list native rules: %w", err)
	}
	localRules := make([]core.Rule, 0, len(rules))
	for _, r := range rules {
		localRules = append(localRules, normalizeRule(accountID, r))
	}
	if err = s.Store.ReplaceProviderRules(ctx, accountID, mail.ID(), localRules); err != nil {
		return result, fmt.Errorf("save native rules: %w", err)
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.Processor != nil {
			allMessages, readErr := s.Store.ConversationMessages(ctx, id)
			if readErr != nil {
				return result, fmt.Errorf("load changed conversation %s: %w", id, readErr)
			}
			conversation := conversations[id]
			if stored, e := s.Store.Conversation(ctx, id); e == nil {
				conversation = stored
			}
			if err = s.Processor.ProcessConversation(ctx, conversation, allMessages, route); err != nil {
				return result, fmt.Errorf("process conversation %s: %w", id, err)
			}
		}
		result.ChangedConversations++
	}
	return result, nil
}

func normalizePage(account string, in []provider.ProviderMessage) ([]core.Message, []core.Conversation) {
	messages := make([]core.Message, 0, len(in))
	byConversation := map[string]core.Conversation{}
	for _, p := range in {
		cid := account + ":" + p.ConversationKey
		id := account + ":" + p.StableID
		m := core.Message{ID: id, AccountID: account, ProviderID: p.StableID, ConversationID: cid, Revision: p.Revision, InternetMessageID: p.InternetMessageID, Subject: p.Subject, Sender: p.Sender, Recipients: p.Recipients, ReceivedAt: p.ReceivedAt.UTC(), Read: p.Read, FolderID: p.FolderID, TagIDs: p.TagIDs}
		messages = append(messages, m)
		c := byConversation[cid]
		c.ID = cid
		c.AccountID = account
		c.ProviderKey = p.ConversationKey
		c.Subject = p.Subject
		c.MessageIDs = append(c.MessageIDs, id)
		if p.ReceivedAt.After(c.LastMessageAt) {
			c.LastMessageAt = p.ReceivedAt
		}
		byConversation[cid] = c
	}
	convs := make([]core.Conversation, 0, len(byConversation))
	for _, c := range byConversation {
		convs = append(convs, c)
	}
	sort.Slice(convs, func(i, j int) bool { return convs[i].ID < convs[j].ID })
	return messages, convs
}
func normalizeRule(account string, r provider.ProviderRule) core.Rule {
	b, _ := json.Marshal(struct {
		Conditions, Exceptions []core.Filter
		Actions                []core.Action
	}{r.Conditions, r.Exceptions, r.Actions})
	sum := sha256.Sum256(b)
	return core.Rule{ID: account + ":" + r.Source + ":" + r.ID, AccountID: account, Source: r.Source, ProviderID: r.ID, Name: r.Name, Enabled: r.Enabled, ReadOnly: r.ReadOnly, Sequence: r.Sequence, Conditions: r.Conditions, Exceptions: r.Exceptions, Actions: r.Actions, RawProvider: r.Raw, CanonicalHash: hex.EncodeToString(sum[:])}
}
