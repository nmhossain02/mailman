package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/nabeel/mailman/internal/core"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var ErrRequestHashMismatch = errors.New("operation execution key reused with different request")

type DB struct{ sql *sql.DB }

type JournalEntry struct {
	ExecutionKey, RequestHash, State string
	Response                         json.RawMessage
	UpdatedAt                        time.Time
}

func Open(ctx context.Context, path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	s := &DB{sql: db}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *DB) Close() error { return s.sql.Close() }

func (s *DB) Migrate(ctx context.Context) error {
	if _, err := s.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = s.sql.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE name=?`, name).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("migration %s checksum mismatch", name)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tx, err := s.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(name, checksum, applied_at) VALUES(?,?,?)`, name, checksum, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *DB) UpsertConversation(ctx context.Context, c core.Conversation) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO conversations(id,account_id,provider_key,subject,last_message_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,provider_key=excluded.provider_key,subject=excluded.subject,last_message_at=excluded.last_message_at`, c.ID, c.AccountID, c.ProviderKey, c.Subject, c.LastMessageAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *DB) Cursor(ctx context.Context, accountID, scope string) (string, bool, error) {
	var checkpoint string
	err := s.sql.QueryRowContext(ctx, `SELECT checkpoint FROM cursors WHERE account_id=? AND scope=?`, accountID, scope).Scan(&checkpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return checkpoint, err == nil, err
}

// PromoteCursor is called only after every page in a provider sync commits.
func (s *DB) PromoteCursor(ctx context.Context, accountID, scope, checkpoint string, now time.Time) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO cursors(account_id,scope,checkpoint,updated_at) VALUES(?,?,?,?) ON CONFLICT(account_id,scope) DO UPDATE SET checkpoint=excluded.checkpoint,updated_at=excluded.updated_at`, accountID, scope, checkpoint, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *DB) UpsertMessage(ctx context.Context, m core.Message) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = upsertMessageTx(ctx, tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertMessageTx(ctx context.Context, tx *sql.Tx, m core.Message) error {
	recipients, err := json.Marshal(m.Recipients)
	if err != nil {
		return err
	}
	tags, err := json.Marshal(m.TagIDs)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,account_id,provider_id,conversation_id,revision,internet_message_id,subject,sender,normalized_body,recipients_json,received_at,is_read,folder_id,tag_ids_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,provider_id=excluded.provider_id,conversation_id=excluded.conversation_id,revision=excluded.revision,internet_message_id=excluded.internet_message_id,subject=excluded.subject,sender=excluded.sender,normalized_body=excluded.normalized_body,recipients_json=excluded.recipients_json,received_at=excluded.received_at,is_read=excluded.is_read,folder_id=excluded.folder_id,tag_ids_json=excluded.tag_ids_json`, m.ID, m.AccountID, m.ProviderID, m.ConversationID, m.Revision, m.InternetMessageID, m.Subject, m.Sender, m.NormalizedBody, recipients, m.ReceivedAt.UTC().Format(time.RFC3339Nano), m.Read, m.FolderID, tags); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM message_search WHERE message_id=?`, m.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO message_search(message_id,sender,subject,normalized_body) VALUES(?,?,?,?)`, m.ID, m.Sender, m.Subject, m.NormalizedBody)
	return err
}

// CommitSyncPage persists a provider page atomically. A non-empty checkpoint is
// supplied only for the final page, so a crash cannot promote partial state.
func (s *DB) CommitSyncPage(ctx context.Context, accountID, scope string, messages []core.Message, conversations []core.Conversation, deleted []string, checkpoint string, now time.Time) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range conversations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,account_id,provider_key,subject,last_message_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,provider_key=excluded.provider_key,subject=excluded.subject,last_message_at=excluded.last_message_at`, c.ID, c.AccountID, c.ProviderKey, c.Subject, c.LastMessageAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	for _, id := range deleted {
		if _, err = tx.ExecContext(ctx, `DELETE FROM message_search WHERE message_id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id); err != nil {
			return err
		}
	}
	for _, m := range messages {
		if err = upsertMessageTx(ctx, tx, m); err != nil {
			return err
		}
	}
	if checkpoint != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO cursors(account_id,scope,checkpoint,updated_at) VALUES(?,?,?,?) ON CONFLICT(account_id,scope) DO UPDATE SET checkpoint=excluded.checkpoint,updated_at=excluded.updated_at`, accountID, scope, checkpoint, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *DB) DeleteMessage(ctx context.Context, id string) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM message_search WHERE message_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DB) SearchMessages(ctx context.Context, query string, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.sql.QueryContext(ctx, `SELECT m.id,m.account_id,m.provider_id,m.conversation_id,m.revision,m.internet_message_id,m.subject,m.sender,m.normalized_body,m.recipients_json,m.received_at,m.is_read,m.folder_id,m.tag_ids_json FROM message_search f JOIN messages m ON m.id=f.message_id WHERE message_search MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner) (core.Message, error) {
	var m core.Message
	var recipients, tags []byte
	var received string
	err := row.Scan(&m.ID, &m.AccountID, &m.ProviderID, &m.ConversationID, &m.Revision, &m.InternetMessageID, &m.Subject, &m.Sender, &m.NormalizedBody, &recipients, &received, &m.Read, &m.FolderID, &tags)
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(recipients, &m.Recipients); err != nil {
		return m, err
	}
	if err = json.Unmarshal(tags, &m.TagIDs); err != nil {
		return m, err
	}
	m.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
	return m, err
}

func (s *DB) Message(ctx context.Context, id string) (core.Message, error) {
	return scanMessage(s.sql.QueryRowContext(ctx, `SELECT id,account_id,provider_id,conversation_id,revision,internet_message_id,subject,sender,normalized_body,recipients_json,received_at,is_read,folder_id,tag_ids_json FROM messages WHERE id=?`, id))
}

func (s *DB) Conversation(ctx context.Context, id string) (core.Conversation, error) {
	var c core.Conversation
	var last string
	err := s.sql.QueryRowContext(ctx, `SELECT id,account_id,provider_key,subject,last_message_at FROM conversations WHERE id=?`, id).Scan(&c.ID, &c.AccountID, &c.ProviderKey, &c.Subject, &last)
	if err != nil {
		return c, err
	}
	c.LastMessageAt, err = time.Parse(time.RFC3339Nano, last)
	if err != nil {
		return c, err
	}
	rows, err := s.ConversationMessages(ctx, id)
	if err != nil {
		return c, err
	}
	for _, m := range rows {
		c.MessageIDs = append(c.MessageIDs, m.ID)
	}
	return c, nil
}

func (s *DB) Conversations(ctx context.Context, accountID string) ([]core.Conversation, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,account_id,provider_key,subject,last_message_at FROM conversations WHERE (?='' OR account_id=?) ORDER BY last_message_at DESC,id`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	var out []core.Conversation
	for rows.Next() {
		var c core.Conversation
		var last string
		if err = rows.Scan(&c.ID, &c.AccountID, &c.ProviderKey, &c.Subject, &last); err != nil {
			rows.Close()
			return nil, err
		}
		c.LastMessageAt, err = time.Parse(time.RFC3339Nano, last)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		messages, e := s.ConversationMessages(ctx, out[i].ID)
		if e != nil {
			return nil, e
		}
		for _, m := range messages {
			out[i].MessageIDs = append(out[i].MessageIDs, m.ID)
		}
	}
	return out, nil
}

func (s *DB) ConversationMessages(ctx context.Context, conversationID string) ([]core.Message, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT id,account_id,provider_id,conversation_id,revision,internet_message_id,subject,sender,normalized_body,recipients_json,received_at,is_read,folder_id,tag_ids_json FROM messages WHERE conversation_id=? ORDER BY received_at,id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *DB) PutTrace(ctx context.Context, trace core.InferenceTrace) error {
	b, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO inference_traces(id,trace_json) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET trace_json=excluded.trace_json`, trace.ID, b)
	return err
}
func (s *DB) Trace(ctx context.Context, id string) (core.InferenceTrace, error) {
	var t core.InferenceTrace
	var b []byte
	err := s.sql.QueryRowContext(ctx, `SELECT trace_json FROM inference_traces WHERE id=?`, id).Scan(&b)
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(b, &t)
	return t, err
}

func (s *DB) BeginOperation(ctx context.Context, executionKey, requestHash string, now time.Time) (JournalEntry, bool, error) {
	var e JournalEntry
	var response []byte
	var updated string
	err := s.sql.QueryRowContext(ctx, `SELECT execution_key,request_hash,state,response_json,updated_at FROM operation_journal WHERE execution_key=?`, executionKey).Scan(&e.ExecutionKey, &e.RequestHash, &e.State, &response, &updated)
	if err == nil {
		if e.RequestHash != requestHash {
			return e, true, ErrRequestHashMismatch
		}
		e.Response = response
		e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		return e, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return e, false, err
	}
	e = JournalEntry{ExecutionKey: executionKey, RequestHash: requestHash, State: "pending", Response: json.RawMessage(`null`), UpdatedAt: now.UTC()}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO operation_journal(execution_key,request_hash,state,response_json,updated_at) VALUES(?,?,?,?,?)`, executionKey, requestHash, e.State, e.Response, e.UpdatedAt.Format(time.RFC3339Nano))
	return e, false, err
}
func (s *DB) FinishOperation(ctx context.Context, executionKey, state string, response json.RawMessage, now time.Time) error {
	switch state {
	case "succeeded", "uncertain", "failed":
	default:
		return fmt.Errorf("invalid terminal operation state %q", state)
	}
	r, err := s.sql.ExecContext(ctx, `UPDATE operation_journal SET state=?,response_json=?,updated_at=? WHERE execution_key=?`, state, response, now.UTC().Format(time.RFC3339Nano), executionKey)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *DB) PutCache(ctx context.Context, key string, output json.RawMessage, now time.Time) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO inference_cache(cache_key,output_json,created_at) VALUES(?,?,?) ON CONFLICT(cache_key) DO UPDATE SET output_json=excluded.output_json,created_at=excluded.created_at`, key, output, now.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *DB) Cache(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var b []byte
	err := s.sql.QueryRowContext(ctx, `SELECT output_json FROM inference_cache WHERE cache_key=?`, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// CorruptMigrationChecksumForTest exposes no SQL handle while allowing the
// checksum guard to be verified against a temporary database.
func (s *DB) CorruptMigrationChecksumForTest(ctx context.Context, name string) error {
	_, err := s.sql.ExecContext(ctx, `UPDATE schema_migrations SET checksum='corrupt' WHERE name=?`, name)
	return err
}
