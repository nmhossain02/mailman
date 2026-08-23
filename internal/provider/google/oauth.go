package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nabeel/mailman/internal/secret"
)

const (
	ScopeGmailModify    = "https://www.googleapis.com/auth/gmail.modify"
	ScopeGmailSettings  = "https://www.googleapis.com/auth/gmail.settings.basic"
	ScopeCalendarEvents = "https://www.googleapis.com/auth/calendar.events"
	ScopeCalendarList   = "https://www.googleapis.com/auth/calendar.calendarlist.readonly"
	ScopeTasks          = "https://www.googleapis.com/auth/tasks"
)

func ScopesFor(integrations ...string) ([]string, error) {
	m := map[string]bool{}
	for _, v := range integrations {
		switch v {
		case "gmail":
			m[ScopeGmailModify] = true
			m[ScopeGmailSettings] = true
		case "google_calendar":
			m[ScopeCalendarEvents] = true
			m[ScopeCalendarList] = true
		case "google_tasks":
			m[ScopeTasks] = true
		default:
			return nil, fmt.Errorf("unknown Google integration %q", v)
		}
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

type OAuthConfig struct{ ClientID, ClientSecret, AuthURL, TokenURL, TokenKey string }
type OAuth struct {
	config OAuthConfig
	store  secret.Store
	http   *http.Client
}

func NewOAuth(config OAuthConfig, store secret.Store, client *http.Client) *OAuth {
	if config.AuthURL == "" {
		config.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &OAuth{config: config, store: store, http: client}
}

type AuthSession struct{ State, Verifier, RedirectURL, URL string }

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (o *OAuth) Authorization(scopes []string, redirectURL string) (AuthSession, error) {
	state, err := randomURLSafe(24)
	if err != nil {
		return AuthSession{}, err
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		return AuthSession{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {o.config.ClientID}, "redirect_uri": {redirectURL}, "response_type": {"code"}, "scope": {strings.Join(scopes, " ")}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "access_type": {"offline"}, "prompt": {"consent"}}
	return AuthSession{State: state, Verifier: verifier, RedirectURL: redirectURL, URL: o.config.AuthURL + "?" + q.Encode()}, nil
}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresIn    int64     `json:"expires_in"`
	Expiry       time.Time `json:"expiry"`
}

func (o *OAuth) Exchange(ctx context.Context, s AuthSession, state, code string) (Token, error) {
	if state == "" || state != s.State {
		return Token{}, fmt.Errorf("OAuth state mismatch")
	}
	values := url.Values{"client_id": {o.config.ClientID}, "client_secret": {o.config.ClientSecret}, "code": {code}, "code_verifier": {s.Verifier}, "grant_type": {"authorization_code"}, "redirect_uri": {s.RedirectURL}}
	return o.exchange(ctx, values, true)
}
func (o *OAuth) Refresh(ctx context.Context) (Token, error) {
	old, err := o.Load(ctx)
	if err != nil {
		return Token{}, err
	}
	if old.RefreshToken == "" {
		return Token{}, fmt.Errorf("stored Google token has no refresh token")
	}
	values := url.Values{"client_id": {o.config.ClientID}, "client_secret": {o.config.ClientSecret}, "refresh_token": {old.RefreshToken}, "grant_type": {"refresh_token"}}
	return o.exchange(ctx, values, true)
}
func (o *OAuth) exchange(ctx context.Context, values url.Values, preserve bool) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.config.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.http.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return Token{}, fmt.Errorf("Google OAuth token exchange returned HTTP %d: %.512s", resp.StatusCode, data)
	}
	var tok Token
	if err = json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return Token{}, err
	}
	if preserve && tok.RefreshToken == "" {
		if old, loadErr := o.Load(ctx); loadErr == nil {
			tok.RefreshToken = old.RefreshToken
		}
	}
	tok.Expiry = time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if err = o.save(ctx, tok); err != nil {
		return Token{}, err
	}
	return tok, nil
}
func (o *OAuth) save(ctx context.Context, t Token) error {
	if o.store == nil {
		return fmt.Errorf("secret store is required")
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return o.store.Set(ctx, o.config.TokenKey, b)
}
func (o *OAuth) Load(ctx context.Context) (Token, error) {
	if o.store == nil {
		return Token{}, fmt.Errorf("secret store is required")
	}
	b, err := o.store.Get(ctx, o.config.TokenKey)
	if err != nil {
		return Token{}, err
	}
	var t Token
	if err = json.Unmarshal(b, &t); err != nil {
		return Token{}, err
	}
	return t, nil
}

// AuthorizeLoopback runs the desktop flow on an ephemeral loopback port. The
// opener may launch a browser; if it fails the URL is still printed for manual use.
func (o *OAuth) AuthorizeLoopback(ctx context.Context, scopes []string, opener func(string) error, out io.Writer) (Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, err
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	session, err := o.Authorization(scopes, redirect)
	if err != nil {
		return Token{}, err
	}
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, session.URL)
	if opener != nil {
		_ = opener(session.URL)
	}
	type result struct {
		code, state string
		err         error
	}
	ch := make(chan result, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if msg := q.Get("error"); msg != "" {
			ch <- result{err: fmt.Errorf("Google authorization failed: %s", msg)}
			http.Error(w, "Authorization failed", http.StatusBadRequest)
			return
		}
		ch <- result{code: q.Get("code"), state: q.Get("state")}
		io.WriteString(w, "Mailman authorization complete. You can close this window.")
	})
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case ch <- result{err: err}:
			default:
			}
		}
	}()
	defer server.Close()
	select {
	case <-ctx.Done():
		return Token{}, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return Token{}, r.err
		}
		if r.code == "" {
			return Token{}, fmt.Errorf("authorization callback omitted code")
		}
		return o.Exchange(ctx, session, r.state, r.code)
	}
}
