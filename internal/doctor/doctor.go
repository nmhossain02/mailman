package doctor

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nmhossain02/mailman/internal/inference"
	"github.com/nmhossain02/mailman/internal/provider"
	"github.com/nmhossain02/mailman/internal/secret"
)

type Check struct{ Name, Status, Message string }
type Pinger interface{ Ping(context.Context) error }

type Inputs struct {
	Database   Pinger
	Secrets    secret.Store
	LocalModel inference.Backend
	Mail       map[string]provider.MailProvider
	Tasks      map[string]provider.TaskTarget
	Calendars  map[string]provider.CalendarTarget
}

func Run(ctx context.Context, in Inputs) []Check {
	var out []Check
	if in.Database == nil {
		out = append(out, bad("database", "not configured"))
	} else if err := in.Database.Ping(ctx); err != nil {
		out = append(out, bad("database", err.Error()))
	} else {
		out = append(out, ok("database", "ready"))
	}
	if in.Secrets == nil {
		out = append(out, bad("keyring", "not configured"))
	} else if _, err := in.Secrets.Get(ctx, "mailman.doctor.nonexistent"); err != nil && !errors.Is(err, secret.ErrNotFound) {
		out = append(out, bad("keyring", err.Error()))
	} else {
		out = append(out, ok("keyring", "available to the logged-in user"))
	}
	if in.LocalModel == nil {
		out = append(out, bad("local-model", "not configured"))
	} else {
		h := in.LocalModel.Health(ctx)
		if h.Ready {
			out = append(out, ok("local-model", h.ModelRevision))
		} else {
			out = append(out, bad("local-model", h.SafeMessage))
		}
	}
	for _, name := range sortedKeys(in.Mail) {
		p := in.Mail[name]
		if _, err := p.Account(ctx); err != nil {
			out = append(out, bad("mail:"+name, err.Error()))
		} else {
			out = append(out, ok("mail:"+name, "refresh and profile succeeded"))
		}
	}
	for _, name := range sortedKeys(in.Tasks) {
		p := in.Tasks[name]
		if _, err := p.ListTaskLists(ctx); err != nil {
			out = append(out, bad("tasks:"+name, err.Error()))
		} else {
			out = append(out, ok("tasks:"+name, "authorized separately"))
		}
	}
	for _, name := range sortedKeys(in.Calendars) {
		p := in.Calendars[name]
		if _, err := p.ListCalendars(ctx); err != nil {
			out = append(out, bad("calendar:"+name, err.Error()))
		} else {
			out = append(out, ok("calendar:"+name, "authorized separately"))
		}
	}
	return out
}
func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func ok(name, msg string) Check { return Check{Name: name, Status: "ok", Message: msg} }
func bad(name, msg string) Check {
	if msg == "" {
		msg = "unavailable"
	}
	return Check{Name: name, Status: "error", Message: msg}
}
func Healthy(checks []Check) error {
	for _, c := range checks {
		if c.Status != "ok" {
			return fmt.Errorf("%s: %s", c.Name, c.Message)
		}
	}
	return nil
}
