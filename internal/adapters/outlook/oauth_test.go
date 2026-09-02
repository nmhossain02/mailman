package outlook

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nmhossain02/mailman/internal/adapters/keyring"
	"golang.org/x/oauth2"
)

func TestOAuthBeginUsesStatePKCEAndPublicClientScopes(t *testing.T) {
	auth, err := (OAuth{ClientID: "desktop", RedirectURL: "http://127.0.0.1:9876/callback"}).Begin()
	if err != nil {
		t.Fatal(err)
	}
	if auth.State == "" || auth.Verifier == "" || auth.Challenge == "" {
		t.Fatal("missing state or PKCE values")
	}
	u, err := url.Parse(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != auth.State || q.Get("code_challenge") != auth.Challenge || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("bad auth query: %v", q)
	}
	if q.Get("client_secret") != "" {
		t.Fatal("public client must not send a secret")
	}
	for _, scope := range OAuthScopes {
		if !strings.Contains(q.Get("scope"), scope) {
			t.Errorf("missing scope %q", scope)
		}
	}
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func TestRotatedRefreshTokenIsPersisted(t *testing.T) {
	store := keyring.NewMemoryStore()
	source := &persistingTokenSource{ctx: context.Background(), source: tokenSourceFunc(func() (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "access", RefreshToken: "rotated"}, nil
	}), store: store, key: "microsoft", lastRefresh: "old"}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Get(context.Background(), "microsoft")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "rotated") {
		t.Fatalf("rotated token not persisted: %s", raw)
	}
}

func TestValidateCallbackRejectsStateMismatch(t *testing.T) {
	if _, err := ValidateCallback("expected", url.Values{"state": {"wrong"}, "code": {"code"}}); err == nil {
		t.Fatal("accepted mismatched state")
	}
	code, err := ValidateCallback("expected", url.Values{"state": {"expected"}, "code": {"code"}})
	if err != nil || code != "code" {
		t.Fatalf("code=%q err=%v", code, err)
	}
}

func TestBrowserFailurePrintsAuthorizationURL(t *testing.T) {
	var out bytes.Buffer
	err := OpenAuthorization(Authorization{URL: "https://login.example/authorize"}, func(string) error { return errors.New("no browser") }, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://login.example/authorize") {
		t.Fatalf("fallback output=%q", out.String())
	}
}
