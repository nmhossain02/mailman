package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"runtime/debug"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nmhossain02/mailman/internal/adapters/google"
	secret "github.com/nmhossain02/mailman/internal/adapters/keyring"
	"github.com/nmhossain02/mailman/internal/adapters/outlook"
	store "github.com/nmhossain02/mailman/internal/adapters/sqlite"
	inference "github.com/nmhossain02/mailman/internal/agent"
	evalpkg "github.com/nmhossain02/mailman/internal/agent/eval"
	"github.com/nmhossain02/mailman/internal/agent/ollama"
	openaiadapter "github.com/nmhossain02/mailman/internal/agent/openai"
	app "github.com/nmhossain02/mailman/internal/application"
	"github.com/nmhossain02/mailman/internal/application/progress"
	"github.com/nmhossain02/mailman/internal/application/provider"
	"github.com/nmhossain02/mailman/internal/automation"
	"github.com/nmhossain02/mailman/internal/cli"
	core "github.com/nmhossain02/mailman/internal/domain"
	"github.com/nmhossain02/mailman/internal/system/config"
	doctor "github.com/nmhossain02/mailman/internal/system/health"
	localinstall "github.com/nmhossain02/mailman/internal/system/install"
	ui "github.com/nmhossain02/mailman/internal/tui"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type runtime struct {
	cfg       config.File
	db        *store.DB
	secrets   secret.Store
	local     inference.Backend
	external  inference.Backend
	mail      map[string]provider.MailProvider
	tasks     map[string]provider.TaskTarget
	calendars map[string]provider.CalendarTarget
	plans     app.PlanService
}

func Run(ctx context.Context, args []string) error {
	req, err := cli.Parse(args)
	if err != nil {
		return err
	}
	if req.Mode == cli.ModeExact && req.Command == "version" {
		info := versionInfo()
		if req.JSON {
			return json.NewEncoder(os.Stdout).Encode(info)
		}
		fmt.Fprintf(os.Stdout, "mailman %s (%s, %s)\n", info.Version, info.Commit, info.Date)
		return nil
	}
	if req.Mode == cli.ModeExact && (req.Command == "install" || req.Command == "uninstall") {
		return runLocalInstall(req.Command)
	}
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dataDir, "config.json")
	configured, err := configExists(cfgPath)
	if err != nil {
		return err
	}
	if (req.Mode == cli.ModeExact && req.Command == "setup") || (req.Mode == cli.ModeTUI && !configured) {
		account, setupErr := config.Setup(os.Stdin, os.Stdout, dataDir)
		if setupErr != nil {
			return setupErr
		}
		fmt.Fprintf(os.Stdout, "Next, authorize the account:\n  Installed:   mailman auth %s\n  From source: go run ./cmd/mailman auth %s\n", account.ID, account.ID)
		return nil
	}
	if !configured && !(req.Mode == cli.ModeExact && req.Command == "eval run") {
		return errors.New("no account is configured; run `mailman setup`")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.Core.DataDir != "" {
		dataDir = cfg.Core.DataDir
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	rt, err := newRuntime(ctx, cfg, filepath.Join(dataDir, "mailman.db"))
	if err != nil {
		return err
	}
	defer rt.db.Close()
	backend := &app.Service{Store: rt.db, Plans: rt.plans, Translator: inference.Translator{Backend: rt.local, Model: rt.cfg.Core.Local.Model}}
	switch req.Mode {
	case cli.ModeTUI:
		_, err = tea.NewProgram(ui.NewModel(ctx, backend)).Run()
		return err
	case cli.ModeNatural:
		interpreted, err := backend.Interpret(ctx, req.NaturalText, backend.CommandContext(ctx))
		if err != nil {
			return err
		}
		return write(os.Stdout, interpreted, false)
	case cli.ModeExact:
		return runExact(ctx, rt, backend, req, dataDir)
	default:
		return errors.New("unsupported command mode")
	}
}

func runLocalInstall(command string) error {
	destination, err := localinstall.Destination()
	if err != nil {
		return err
	}
	if command == "uninstall" {
		if err = localinstall.Uninstall(destination); errors.Is(err, localinstall.ErrNotInstalled) {
			fmt.Fprintln(os.Stdout, "Mailman is not installed locally.")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Removed %s\n", destination)
		return nil
	}
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running executable: %w", err)
	}
	if err = localinstall.Install(source, destination); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Installed %s\n", destination)
	if !localinstall.DirectoryInPath(destination, os.Getenv("PATH")) {
		fmt.Fprintln(os.Stdout, `Add Mailman to PATH: export PATH="$HOME/.local/bin:$PATH"`)
	}
	return nil
}

func configExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect config: %w", err)
}

func newRuntime(ctx context.Context, cfg config.File, dbPath string) (*runtime, error) {
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	keys, err := secret.NewKeyringStore("mailman")
	if err != nil {
		db.Close()
		return nil, err
	}
	local := &ollama.Backend{BaseURL: cfg.Core.Local.BaseURL, Model: cfg.Core.Local.Model, Client: &http.Client{Timeout: duration(cfg.Core.Local.BackgroundTimeoutSeconds, 120)}}
	rt := &runtime{cfg: cfg, db: db, secrets: keys, local: local, mail: map[string]provider.MailProvider{}, tasks: map[string]provider.TaskTarget{}, calendars: map[string]provider.CalendarTarget{}}
	if cfg.Core.External.Enabled {
		key, loadErr := keys.Get(ctx, "openai.api_key")
		if loadErr == nil {
			rt.external = &openaiadapter.Backend{BaseURL: cfg.Core.External.BaseURL, APIKey: string(key), Client: &http.Client{Timeout: duration(cfg.Core.External.ExternalTimeoutSeconds, 90)}}
		}
	}
	for _, account := range cfg.Accounts {
		if !account.Enabled {
			continue
		}
		switch account.Provider {
		case "gmail":
			oauth := google.NewOAuth(google.OAuthConfig{ClientID: cfg.Core.Google.ClientID, ClientSecret: cfg.Core.Google.ClientSecret, TokenKey: account.TokenKey}, keys, nil)
			client := &http.Client{Transport: &googleTransport{o: oauth, base: http.DefaultTransport}}
			rt.mail[account.ID] = google.NewGmail(client, "", account.ID)
			if contains(account.Integrations, "google_tasks") {
				rt.tasks[account.ID] = google.NewTasks(client, "", account.TaskListID)
			}
			if contains(account.Integrations, "google_calendar") {
				rt.calendars[account.ID] = google.NewCalendar(client, "", account.CalendarID)
			}
		case "outlook":
			oauth := outlook.OAuth{ClientID: cfg.Core.Microsoft.ClientID, Tenant: cfg.Core.Microsoft.Tenant, Store: keys, TokenKey: account.TokenKey}
			source, e := oauth.TokenSource(ctx)
			if e != nil {
				continue
			}
			rt.mail[account.ID] = outlook.NewClient("https://graph.microsoft.com/v1.0", nil, outlook.AccessTokenFunc(source))
		}
	}
	rt.plans = app.PlanService{Store: db, Mail: rt.mail, Tasks: rt.tasks, Calendars: rt.calendars}
	return rt, nil
}

type googleTransport struct {
	o    *google.OAuth
	base http.RoundTripper
	mu   sync.Mutex
}

func (t *googleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	token, err := t.o.Load(req.Context())
	if err == nil && time.Until(token.Expiry) < time.Minute {
		token, err = t.o.Refresh(req.Context())
	}
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	copyReq := req.Clone(req.Context())
	copyReq.Header = req.Header.Clone()
	copyReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	return t.base.RoundTrip(copyReq)
}

func runExact(ctx context.Context, rt *runtime, b *app.Service, req cli.Request, dataDir string) error {
	switch req.Command {
	case "auth":
		return authorize(ctx, rt, req.Name)
	case "doctor":
		checks := doctor.Run(ctx, doctor.Inputs{Database: rt.db, Secrets: rt.secrets, LocalModel: rt.local, Mail: rt.mail, Tasks: rt.tasks, Calendars: rt.calendars})
		if req.JSON {
			return write(os.Stdout, checks, true)
		}
		for _, c := range checks {
			fmt.Fprintf(os.Stdout, "%-16s %-6s %s\n", c.Name, c.Status, c.Message)
		}
		fmt.Fprintln(os.Stdout, "Scheduled commands must run as the logged-in user with the OS keyring unlocked.")
		return doctor.Healthy(checks)
	case "sync":
		service := app.SyncService{Store: rt.db}
		results := map[string]app.SyncResult{}
		for id, p := range rt.mail {
			syncCtx := progress.WithReporter(ctx, syncProgressReporter(os.Stderr, id))
			r, err := service.Sync(syncCtx, id, "mail", p, rt.cfg.Core.Routing)
			if err != nil {
				return err
			}
			results[id] = r
		}
		if len(results) == 0 {
			return errors.New("no enabled mail accounts; edit config.json and authorize a provider")
		}
		return write(os.Stdout, results, req.JSON)
	case "schedule run":
		preparer := app.ScheduledPreparer{Sync: app.SyncService{Store: rt.db}, Plans: rt.plans, Store: rt.db, Mail: rt.mail}
		result, err := (automation.Runner{Store: rt.db, Preparer: preparer}).Run(ctx, req.Name)
		if err != nil {
			return err
		}
		return write(os.Stdout, result, req.JSON)
	case "eval run":
		return runEval(ctx, rt, req, dataDir)
	default:
		return fmt.Errorf("unsupported exact command %q", req.Command)
	}
}

func syncProgressReporter(out io.Writer, account string) progress.Reporter {
	return func(event progress.Event) {
		switch event.Stage {
		case progress.StageStarting:
			mode := "full"
			if event.Incremental {
				mode = "incremental"
			}
			fmt.Fprintf(out, "sync %s: starting %s sync\n", account, mode)
		case progress.StageFetchingPage:
			fmt.Fprintf(out, "sync %s: fetching page %d\n", account, event.Current)
		case progress.StageMetadata, progress.StageContent:
			if event.Current != 1 && event.Current != event.Total && event.Current%50 != 0 {
				return
			}
			fmt.Fprintf(out, "sync %s: %s %d/%d\n", account, event.Stage, event.Current, event.Total)
		case progress.StagePageCommitted:
			fmt.Fprintf(out, "sync %s: committed page %d (%d messages)\n", account, event.Pages, event.Messages)
		case progress.StageRules:
			fmt.Fprintf(out, "sync %s: refreshing provider rules\n", account)
		case progress.StageDone:
			fmt.Fprintf(out, "sync %s: complete (%d pages, %d messages, %d conversations)\n", account, event.Pages, event.Messages, event.Conversations)
		}
	}
}

type buildInfo struct{ Version, Commit, Date string }

func versionInfo() buildInfo {
	v := version
	c := commit
	d := buildDate
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if c == "unknown" {
					c = setting.Value
				}
			case "vcs.time":
				if d == "unknown" {
					d = setting.Value
				}
			}
		}
	}
	return buildInfo{Version: v, Commit: c, Date: d}
}

func authorize(ctx context.Context, rt *runtime, name string) error {
	var account *config.Account
	for i := range rt.cfg.Accounts {
		if rt.cfg.Accounts[i].ID == name || rt.cfg.Accounts[i].Name == name {
			account = &rt.cfg.Accounts[i]
			break
		}
	}
	if account == nil {
		return fmt.Errorf("account %q not found in config", name)
	}
	switch account.Provider {
	case "gmail":
		integrations := append([]string{"gmail"}, account.Integrations...)
		scopes, err := google.ScopesFor(integrations...)
		if err != nil {
			return err
		}
		oauth := google.NewOAuth(google.OAuthConfig{ClientID: rt.cfg.Core.Google.ClientID, ClientSecret: rt.cfg.Core.Google.ClientSecret, TokenKey: account.TokenKey}, rt.secrets, nil)
		if _, err = oauth.AuthorizeLoopback(ctx, scopes, openBrowser, os.Stdout); err != nil {
			return err
		}
		if err = rt.db.SaveGrant(ctx, core.IntegrationGrant{ID: "grant:" + account.ID, AccountID: account.ID, Kind: "google", TokenKey: account.TokenKey, GrantedScopes: scopes, Enabled: true}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Google authorization saved in the OS keyring.")
		return nil
	case "outlook":
		redirect := account.RedirectURL
		if redirect == "" {
			redirect = "http://127.0.0.1:53682/oauth/callback"
		}
		oauth := outlook.OAuth{ClientID: rt.cfg.Core.Microsoft.ClientID, Tenant: rt.cfg.Core.Microsoft.Tenant, RedirectURL: redirect, Store: rt.secrets, TokenKey: account.TokenKey}
		auth, err := oauth.Begin()
		if err != nil {
			return err
		}
		_ = outlook.OpenAuthorization(auth, openBrowser, os.Stdout)
		u, err := neturl.Parse(redirect)
		if err != nil {
			return err
		}
		code, err := outlook.ServeLoopback(ctx, u.Host, u.Path, auth.State)
		if err != nil {
			return err
		}
		if _, err = oauth.Exchange(ctx, code, auth.Verifier); err != nil {
			return err
		}
		if err = rt.db.SaveGrant(ctx, core.IntegrationGrant{ID: "grant:" + account.ID, AccountID: account.ID, Kind: "microsoft", TokenKey: account.TokenKey, GrantedScopes: outlook.OAuthScopes, Enabled: true}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Microsoft authorization saved in the OS keyring.")
		return nil
	default:
		return fmt.Errorf("unsupported provider %q", account.Provider)
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch stdruntime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func runEval(ctx context.Context, rt *runtime, req cli.Request, dataDir string) error {
	path := filepath.Join(dataDir, "eval.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	records, err := evalpkg.ReadJSONL(file)
	if err != nil {
		return err
	}
	mode := evalpkg.RouteLocalOnly
	if req.AllowExternal {
		mode = evalpkg.RouteProbe
	}
	cfg := evalpkg.Snapshot("cli-"+time.Now().UTC().Format("20060102T150405Z"), filepath.Base(path), mode)
	cfg.LocalBackend, cfg.LocalModel = "ollama", rt.cfg.Core.Local.Model
	cfg.ExternalBackend, cfg.ExternalModel = "openai", rt.cfg.Core.External.Model
	cfg.MaxExternalCalls = req.MaxExternalCalls
	execute := func(ctx context.Context, c inference.EvalCase, class string) evalpkg.Observation {
		task, e := inference.BuiltinTask(c.TaskName, map[bool]string{true: rt.cfg.Core.External.Model, false: rt.cfg.Core.Local.Model}[class == "external"])
		if e != nil {
			return evalpkg.Observation{Outcome: "invalid", ErrorKind: "invalid_request"}
		}
		chosen := rt.local
		if class == "external" {
			chosen = rt.external
		}
		result, e := inference.RunTask(ctx, chosen, task, c.InputJSON, c.ID)
		if e != nil {
			return evalpkg.Observation{Outcome: "error", ErrorKind: inferenceErrorKind(e)}
		}
		output, _ := json.Marshal(result.Output)
		return evalpkg.Observation{Outcome: result.Outcome, Output: output, InputTokens: result.Raw.InputTokens, CachedTokens: result.Raw.CachedInputTokens, OutputTokens: result.Raw.OutputTokens, WallMS: result.Raw.WallMS, GenerationMS: result.Raw.GenerationMS}
	}
	result, err := evalpkg.Run(ctx, cfg, records, execute)
	if err != nil {
		return err
	}
	if req.JSON {
		return evalpkg.WriteJSON(os.Stdout, result)
	}
	return evalpkg.WriteTable(os.Stdout, result)
}

func duration(seconds, fallback int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	return time.Duration(seconds) * time.Second
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func inferenceErrorKind(err error) string {
	var e *inference.InferenceError
	if errors.As(err, &e) {
		return e.Kind
	}
	return "error"
}
func write(w *os.File, v any, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(w).Encode(v)
	}
	switch x := v.(type) {
	case ui.Interpretation:
		b, _ := json.MarshalIndent(x.Draft, "", "  ")
		fmt.Fprintln(w, string(b))
		fmt.Fprintln(w, "Review this compiled command in the TUI before creating a plan.")
		return nil
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(w, string(b))
		return nil
	}
}
