package outlook

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nmhossain02/mailman/internal/adapters/keyring"
	"golang.org/x/oauth2"
)

var OAuthScopes = []string{"openid", "profile", "offline_access", "Mail.ReadWrite", "MailboxSettings.ReadWrite"}

type OAuth struct {
	ClientID, Tenant, RedirectURL string
	Store                         keyring.Store
	TokenKey                      string
	HTTPClient                    *http.Client
}

func (o OAuth) tenant() string {
	if o.Tenant == "" {
		return "common"
	}
	return o.Tenant
}
func (o OAuth) config() *oauth2.Config {
	return &oauth2.Config{ClientID: o.ClientID, RedirectURL: o.RedirectURL, Scopes: OAuthScopes, Endpoint: oauth2.Endpoint{AuthURL: "https://login.microsoftonline.com/" + url.PathEscape(o.tenant()) + "/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/" + url.PathEscape(o.tenant()) + "/oauth2/v2.0/token"}}
}

type Authorization struct{ State, Verifier, Challenge, URL string }

func (o OAuth) Begin() (Authorization, error) {
	state, err := randomURL(24)
	if err != nil {
		return Authorization{}, err
	}
	verifier, err := randomURL(48)
	if err != nil {
		return Authorization{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	u := o.config().AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256"), oauth2.SetAuthURLParam("prompt", "select_account"))
	return Authorization{state, verifier, challenge, u}, nil
}

// OpenAuthorization keeps browser-launch fallback behavior inside the adapter.
// A nil or failing opener is not fatal because the printed URL is sufficient.
func OpenAuthorization(auth Authorization, opener func(string) error, fallback io.Writer) error {
	if opener != nil {
		if err := opener(auth.URL); err == nil {
			return nil
		}
	}
	if fallback == nil {
		return errors.New("browser launch failed and no authorization URL writer was provided")
	}
	_, err := fmt.Fprintln(fallback, auth.URL)
	return err
}
func randomURL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func ValidateCallback(expectedState string, values url.Values) (string, error) {
	if values.Get("state") == "" || values.Get("state") != expectedState {
		return "", errors.New("OAuth state mismatch")
	}
	if e := values.Get("error"); e != "" {
		return "", fmt.Errorf("OAuth authorization failed: %s", e)
	}
	code := values.Get("code")
	if code == "" {
		return "", errors.New("OAuth callback missing code")
	}
	return code, nil
}
func (o OAuth) Exchange(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	if o.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, o.HTTPClient)
	}
	token, err := o.config().Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return nil, err
	}
	if err := o.save(ctx, token); err != nil {
		return nil, err
	}
	return token, nil
}
func (o OAuth) save(ctx context.Context, t *oauth2.Token) error {
	if o.Store == nil {
		return errors.New("OAuth secret store is required")
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return o.Store.Set(ctx, o.TokenKey, b)
}
func (o OAuth) Load(ctx context.Context) (*oauth2.Token, error) {
	if o.Store == nil {
		return nil, errors.New("OAuth secret store is required")
	}
	b, err := o.Store.Get(ctx, o.TokenKey)
	if err != nil {
		return nil, err
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (o OAuth) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	token, err := o.Load(ctx)
	if err != nil {
		return nil, err
	}
	if o.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, o.HTTPClient)
	}
	source := o.config().TokenSource(ctx, token)
	return &persistingTokenSource{ctx: ctx, source: source, store: o.Store, key: o.TokenKey, lastRefresh: token.RefreshToken}, nil
}

type persistingTokenSource struct {
	ctx              context.Context
	source           oauth2.TokenSource
	store            keyring.Store
	key, lastRefresh string
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	t, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if t.RefreshToken != "" && t.RefreshToken != s.lastRefresh {
		b, _ := json.Marshal(t)
		if err := s.store.Set(s.ctx, s.key, b); err != nil {
			return nil, err
		}
		s.lastRefresh = t.RefreshToken
	}
	return t, nil
}

// ServeLoopback validates the callback and returns the authorization code. The
// caller owns opening Authorization.URL; if that fails it can print the URL.
func ServeLoopback(ctx context.Context, listenAddr, path, state string) (string, error) {
	server := &http.Server{Addr: listenAddr}
	codes := make(chan string, 1)
	errs := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		code, err := ValidateCallback(state, r.URL.Query())
		if err != nil {
			http.Error(w, "Authorization failed", http.StatusBadRequest)
			errs <- err
			return
		}
		_, _ = w.Write([]byte("Authorization complete. You may close this window."))
		codes <- code
	})
	server.Handler = mux
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errs <- err:
			default:
			}
		}
	}()
	select {
	case code := <-codes:
		_ = server.Shutdown(context.Background())
		return code, nil
	case err := <-errs:
		_ = server.Shutdown(context.Background())
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func AccessTokenFunc(source oauth2.TokenSource) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		t, err := source.Token()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(t.AccessToken), nil
	}
}
