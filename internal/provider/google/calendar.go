package google

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/provider"
)

type Calendar struct {
	api             apiClient
	defaultCalendar string
}

func NewCalendar(client *http.Client, baseURL, defaultCalendar string) *Calendar {
	if baseURL == "" {
		baseURL = "https://www.googleapis.com/calendar/v3"
	}
	return &Calendar{api: newAPIClient(client, strings.TrimRight(baseURL, "/")), defaultCalendar: defaultCalendar}
}

func (c *Calendar) ListCalendars(ctx context.Context) ([]provider.Calendar, error) {
	var out []provider.Calendar
	token := ""
	for {
		q := url.Values{"maxResults": {"250"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var r struct {
			Items         []struct{ ID, Summary, AccessRole string }
			NextPageToken string
		}
		if _, err := c.api.do(ctx, http.MethodGet, "/users/me/calendarList", q, nil, &r); err != nil {
			return nil, err
		}
		for _, v := range r.Items {
			out = append(out, provider.Calendar{ID: v.ID, Name: v.Summary, Writable: v.AccessRole == "writer" || v.AccessRole == "owner"})
		}
		if r.NextPageToken == "" {
			return out, nil
		}
		token = r.NextPageToken
	}
}

func EventID(executionKey string) string {
	sum := sha256.Sum256([]byte(executionKey))
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:20]))
}

type eventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}
type googleEvent struct {
	ID                 string    `json:"id,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	Description        string    `json:"description,omitempty"`
	Location           string    `json:"location,omitempty"`
	Start              eventTime `json:"start"`
	End                eventTime `json:"end"`
	ExtendedProperties struct {
		Private map[string]string `json:"private,omitempty"`
	} `json:"extendedProperties,omitempty"`
}

func eventBody(d provider.EventDraft, key string) (googleEvent, error) {
	var e googleEvent
	e.ID = EventID(key)
	e.Summary = d.Title
	e.Description = d.Description
	e.Location = d.Location
	e.ExtendedProperties.Private = map[string]string{"mailman_execution_key": key, "mailman_source": "mailman"}
	if d.AllDay {
		if _, err := time.Parse("2006-01-02", d.Start); err != nil {
			return e, fmt.Errorf("all-day start must be a date: %w", err)
		}
		if _, err := time.Parse("2006-01-02", d.End); err != nil {
			return e, fmt.Errorf("all-day end must be an exclusive date: %w", err)
		}
		e.Start.Date = d.Start
		e.End.Date = d.End
		return e, nil
	}
	if d.Timezone == "" {
		return e, fmt.Errorf("timezone is required for timed events")
	}
	if _, err := time.LoadLocation(d.Timezone); err != nil {
		return e, fmt.Errorf("invalid timezone: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, d.Start); err != nil {
		return e, fmt.Errorf("timed start must be RFC3339: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, d.End); err != nil {
		return e, fmt.Errorf("timed end must be RFC3339: %w", err)
	}
	e.Start.DateTime = d.Start
	e.Start.TimeZone = d.Timezone
	e.End.DateTime = d.End
	e.End.TimeZone = d.Timezone
	return e, nil
}

func (c *Calendar) EnsureEvent(ctx context.Context, d provider.EventDraft, key string) (provider.TargetReceipt, error) {
	cal := d.CalendarID
	if cal == "" {
		cal = c.defaultCalendar
	}
	if cal == "" {
		return provider.TargetReceipt{}, fmt.Errorf("calendar is required")
	}
	body, err := eventBody(d, key)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	var created googleEvent
	if _, err = c.api.do(ctx, http.MethodPut, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(body.ID), nil, body, &created); err != nil {
		return provider.TargetReceipt{ProviderID: joinTarget(cal, body.ID), ExecutionKey: key, Status: "failed"}, err
	}
	raw, _ := json.Marshal(created)
	return provider.TargetReceipt{ProviderID: joinTarget(cal, body.ID), ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
}

func (c *Calendar) UpdateEvent(ctx context.Context, id string, p provider.EventPatch) (provider.TargetReceipt, error) {
	d := provider.EventDraft(p)
	cal, item, err := splitTarget(id, d.CalendarID)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	body, err := eventBody(d, "update:"+item)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	body.ID = ""
	body.ExtendedProperties.Private = nil
	var updated googleEvent
	if _, err = c.api.do(ctx, http.MethodPatch, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(item), nil, body, &updated); err != nil {
		return provider.TargetReceipt{}, err
	}
	raw, _ := json.Marshal(updated)
	return provider.TargetReceipt{ProviderID: joinTarget(cal, item), Status: "succeeded", Raw: raw}, nil
}
func (c *Calendar) DeleteEvent(ctx context.Context, id string) error {
	cal, item, err := splitTarget(id, c.defaultCalendar)
	if err != nil {
		return err
	}
	_, err = c.api.do(ctx, http.MethodDelete, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(item), nil, nil, nil)
	return err
}

var _ provider.CalendarTarget = (*Calendar)(nil)
