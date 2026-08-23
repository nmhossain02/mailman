package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nmhossain02/mailman/internal/app"
	"github.com/nmhossain02/mailman/internal/cli"
	"github.com/nmhossain02/mailman/internal/config"
	"github.com/nmhossain02/mailman/internal/core"
	"github.com/nmhossain02/mailman/internal/doctor"
	evalpkg "github.com/nmhossain02/mailman/internal/eval"
	"github.com/nmhossain02/mailman/internal/inference"
	"github.com/nmhossain02/mailman/internal/inference/ollama"
	openaiadapter "github.com/nmhossain02/mailman/internal/inference/openai"
	"github.com/nmhossain02/mailman/internal/policy"
	"github.com/nmhossain02/mailman/internal/provider"
	"github.com/nmhossain02/mailman/internal/provider/google"
	"github.com/nmhossain02/mailman/internal/provider/outlook"
	"github.com/nmhossain02/mailman/internal/schedule"
	"github.com/nmhossain02/mailman/internal/secret"
	"github.com/nmhossain02/mailman/internal/store"
	"github.com/nmhossain02/mailman/internal/ui"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mailman:", err)
		os.Exit(1)
	}
}

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

func run(ctx context.Context, args []string) error {
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
	dataDir, err := core.DefaultDataDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dataDir, "config.json")
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
	backend := &backend{rt: rt}
	switch req.Mode {
	case cli.ModeTUI:
		_, err = tea.NewProgram(ui.NewModel(ctx, backend)).Run()
		return err
	case cli.ModeNatural:
		interpreted, err := backend.Interpret(ctx, req.NaturalText, translationContext(ctx, rt.db))
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

func runExact(ctx context.Context, rt *runtime, b *backend, req cli.Request, dataDir string) error {
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
			r, err := service.Sync(ctx, id, "mail", p, rt.cfg.Core.Routing)
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
		result, err := (schedule.Runner{Store: rt.db, Preparer: preparer}).Run(ctx, req.Name)
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

type buildInfo struct{ Version, Commit, Date string }

func versionInfo() buildInfo {
	v := version
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
	}
	return buildInfo{Version: v, Commit: commit, Date: buildDate}
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
	execute := func(ctx context.Context, c core.EvalCase, class string) evalpkg.Observation {
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

type backend struct{ rt *runtime }

func (b *backend) ListConversations(ctx context.Context, _ core.CommandDraft) ([]core.Conversation, error) {
	return b.rt.db.Conversations(ctx, "")
}
func (b *backend) GetConversation(ctx context.Context, id string) (ui.ConversationDetail, error) {
	c, err := b.rt.db.Conversation(ctx, id)
	if err != nil {
		return ui.ConversationDetail{}, err
	}
	messages, err := b.rt.db.ConversationMessages(ctx, id)
	if err != nil {
		return ui.ConversationDetail{}, err
	}
	claims, err := b.rt.db.Claims(ctx, "conversation", id)
	return ui.ConversationDetail{Conversation: c, Messages: messages, Claims: claims}, err
}
func (b *backend) ListRules(ctx context.Context) ([]core.Rule, error) { return b.rt.db.Rules(ctx) }
func (b *backend) ListSchedules(ctx context.Context) ([]core.Schedule, error) {
	return b.rt.db.Schedules(ctx)
}
func (b *backend) ListPlans(ctx context.Context) ([]core.Plan, error) { return b.rt.db.Plans(ctx) }
func (b *backend) Interpret(ctx context.Context, text string, tc core.TranslationContext) (ui.Interpretation, error) {
	translator := inference.Translator{Backend: b.rt.local, Model: b.rt.cfg.Core.Local.Model}
	draft, err := translator.Translate(ctx, text, tc)
	if err != nil {
		return ui.Interpretation{}, err
	}
	canonical, _ := json.Marshal(draft)
	return ui.Interpretation{Draft: draft, TraceID: traceID(text), Canonical: canonical}, nil
}
func (b *backend) Preview(ctx context.Context, d core.CommandDraft) (ui.PlanPreview, error) {
	conversations, err := b.rt.db.Conversations(ctx, "")
	if err != nil {
		return ui.PlanPreview{}, err
	}
	rule := core.Rule{ID: "interactive", Name: "interactive", Enabled: true, Conditions: d.Filters, Actions: d.Actions}
	var candidates []policy.Candidate
	scope := 0
	for _, c := range conversations {
		if d.Reference != "" && d.Reference != c.ID {
			continue
		}
		messages, e := b.rt.db.ConversationMessages(ctx, c.ID)
		if e != nil {
			return ui.PlanPreview{}, e
		}
		for _, m := range messages {
			derived := policy.DeriveContext(messages, "", time.Now(), false)
			derived.Message = m
			matched := policy.Evaluate([]core.Rule{rule}, derived)
			if len(matched) > 0 {
				scope++
			}
			candidates = append(candidates, matched...)
		}
	}
	plan, err := b.rt.plans.Draft(ctx, "interactive review", policy.Resolve(candidates, nil))
	if err != nil {
		return ui.PlanPreview{}, err
	}
	groups := groupOperations(plan.Operations)
	return ui.PlanPreview{Plan: plan, ScopeCount: scope, Groups: groups}, nil
}
func (b *backend) FreezePlan(ctx context.Context, id string) (core.Plan, error) {
	p, err := findPlan(ctx, b.rt.db, id)
	if err != nil {
		return p, err
	}
	return b.rt.plans.Freeze(ctx, p)
}
func (b *backend) DecidePlan(ctx context.Context, id string, approved, rejected []string) (core.Plan, error) {
	p, err := findPlan(ctx, b.rt.db, id)
	if err != nil {
		return p, err
	}
	a, r := map[string]bool{}, map[string]bool{}
	for _, v := range approved {
		a[v] = true
	}
	for _, v := range rejected {
		r[v] = true
	}
	return b.rt.plans.Decide(ctx, p, a, r)
}
func (b *backend) ApplyPlan(ctx context.Context, id string) (core.Plan, error) {
	p, err := findPlan(ctx, b.rt.db, id)
	if err != nil {
		return p, err
	}
	return b.rt.plans.Apply(ctx, p)
}
func (b *backend) SaveEvalLabel(ctx context.Context, v core.EvalLabel) error {
	return b.rt.db.SaveEvalLabel(ctx, v)
}

func findPlan(ctx context.Context, db *store.DB, id string) (core.Plan, error) {
	plans, err := db.Plans(ctx)
	if err != nil {
		return core.Plan{}, err
	}
	for _, p := range plans {
		if p.ID == id {
			return p, nil
		}
	}
	return core.Plan{}, fmt.Errorf("plan %q not found", id)
}
func groupOperations(ops []core.Operation) []ui.PlanGroup {
	groups := map[string][]core.Operation{}
	for _, op := range ops {
		groups[op.Kind] = append(groups[op.Kind], op)
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ui.PlanGroup, 0, len(names))
	for _, n := range names {
		g := ui.PlanGroup{Name: n, Count: len(groups[n]), Operations: groups[n]}
		for i := 0; i < len(groups[n]) && i < 3; i++ {
			g.Samples = append(g.Samples, groups[n][i].TargetID)
		}
		out = append(out, g)
	}
	return out
}
func translationContext(ctx context.Context, db *store.DB) core.TranslationContext {
	tc := core.TranslationContext{Now: time.Now(), Timezone: time.Now().Location().String()}
	rules, _ := db.Rules(ctx)
	for _, r := range rules {
		tc.RuleNames = append(tc.RuleNames, r.Name)
	}
	schedules, _ := db.Schedules(ctx)
	for _, s := range schedules {
		tc.ScheduleNames = append(tc.ScheduleNames, s.Name)
	}
	return tc
}
func traceID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("command-%x", sum[:8])
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

var _ ui.Backend = (*backend)(nil)
var _ ui.PlanDecisionBackend = (*backend)(nil)
