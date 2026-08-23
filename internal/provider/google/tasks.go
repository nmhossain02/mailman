package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nabeel/mailman/internal/provider"
)

type Tasks struct {
	api         apiClient
	defaultList string
}

func NewTasks(client *http.Client, baseURL, defaultList string) *Tasks {
	if baseURL == "" {
		baseURL = "https://tasks.googleapis.com/tasks/v1"
	}
	return &Tasks{api: newAPIClient(client, strings.TrimRight(baseURL, "/")), defaultList: defaultList}
}

type googleTask struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Notes  string `json:"notes,omitempty"`
	Due    string `json:"due,omitempty"`
	Status string `json:"status,omitempty"`
}

func (t *Tasks) ListTaskLists(ctx context.Context) ([]provider.TaskList, error) {
	var out []provider.TaskList
	token := ""
	for {
		q := url.Values{"maxResults": {"100"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var r struct {
			Items         []struct{ ID, Title string }
			NextPageToken string
		}
		if _, err := t.api.do(ctx, http.MethodGet, "/users/@me/lists", q, nil, &r); err != nil {
			return nil, err
		}
		for _, v := range r.Items {
			out = append(out, provider.TaskList{ID: v.ID, Name: v.Title})
		}
		if r.NextPageToken == "" {
			return out, nil
		}
		token = r.NextPageToken
	}
}

func normalizeDue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", fmt.Errorf("task due date must be YYYY-MM-DD: %w", err)
	}
	return d.UTC().Format(time.RFC3339), nil
}
func marker(key string) string { return "mailman:" + key }
func withMarker(notes, key string) string {
	m := marker(key)
	if strings.Contains(notes, m) {
		return notes
	}
	if notes == "" {
		return m
	}
	return notes + "\n\n" + m
}

func (t *Tasks) EnsureTask(ctx context.Context, d provider.TaskDraft, key string) (provider.TargetReceipt, error) {
	list := d.ListID
	if list == "" {
		list = t.defaultList
	}
	if list == "" {
		return provider.TargetReceipt{}, fmt.Errorf("task list is required")
	}
	if found, err := t.findMarker(ctx, list, key); err != nil {
		return provider.TargetReceipt{}, err
	} else if found != nil {
		raw, _ := json.Marshal(found)
		return provider.TargetReceipt{ProviderID: joinTarget(list, found.ID), ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
	}
	due, err := normalizeDue(d.DueDate)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	body := googleTask{Title: d.Title, Notes: withMarker(d.Notes, key), Due: due}
	var created googleTask
	_, err = t.api.do(ctx, http.MethodPost, "/lists/"+url.PathEscape(list)+"/tasks", nil, body, &created)
	if err == nil {
		raw, _ := json.Marshal(created)
		return provider.TargetReceipt{ProviderID: joinTarget(list, created.ID), ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
	}
	// Insert is not safely retryable. Reconcile every page before returning uncertain.
	found, reconcileErr := t.findMarker(ctx, list, key)
	if reconcileErr == nil && found != nil {
		raw, _ := json.Marshal(found)
		return provider.TargetReceipt{ProviderID: joinTarget(list, found.ID), ExecutionKey: key, Status: "succeeded", Raw: raw}, nil
	}
	return provider.TargetReceipt{ExecutionKey: key, Status: "uncertain"}, err
}

func (t *Tasks) findMarker(ctx context.Context, list, key string) (*googleTask, error) {
	token := ""
	for {
		q := url.Values{"maxResults": {"100"}, "showCompleted": {"true"}, "showHidden": {"true"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var r struct {
			Items         []googleTask
			NextPageToken string
		}
		if _, err := t.api.do(ctx, http.MethodGet, "/lists/"+url.PathEscape(list)+"/tasks", q, nil, &r); err != nil {
			return nil, err
		}
		for i := range r.Items {
			if strings.Contains(r.Items[i].Notes, marker(key)) {
				return &r.Items[i], nil
			}
		}
		if r.NextPageToken == "" {
			return nil, nil
		}
		token = r.NextPageToken
	}
}

func (t *Tasks) UpdateTask(ctx context.Context, id string, p provider.TaskPatch) (provider.TargetReceipt, error) {
	list, item, err := splitTarget(id, t.defaultList)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	due, err := normalizeDue(p.DueDate)
	if err != nil {
		return provider.TargetReceipt{}, err
	}
	body := googleTask{Title: p.Title, Notes: p.Notes, Due: due, Status: p.Status}
	var updated googleTask
	if _, err = t.api.do(ctx, http.MethodPatch, "/lists/"+url.PathEscape(list)+"/tasks/"+url.PathEscape(item), nil, body, &updated); err != nil {
		return provider.TargetReceipt{}, err
	}
	raw, _ := json.Marshal(updated)
	return provider.TargetReceipt{ProviderID: joinTarget(list, item), Status: "succeeded", Raw: raw}, nil
}
func (t *Tasks) DeleteTask(ctx context.Context, id string) error {
	list, item, err := splitTarget(id, t.defaultList)
	if err != nil {
		return err
	}
	_, err = t.api.do(ctx, http.MethodDelete, "/lists/"+url.PathEscape(list)+"/tasks/"+url.PathEscape(item), nil, nil, nil)
	return err
}

func joinTarget(parent, id string) string { return url.PathEscape(parent) + "/" + url.PathEscape(id) }
func splitTarget(value, fallback string) (string, string, error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) == 1 {
		if fallback == "" {
			return "", "", fmt.Errorf("target ID must include its parent collection")
		}
		return fallback, value, nil
	}
	p, _ := url.PathUnescape(parts[0])
	id, _ := url.PathUnescape(parts[1])
	if p == "" || id == "" {
		return "", "", fmt.Errorf("invalid target ID")
	}
	return p, id, nil
}

var _ provider.TaskTarget = (*Tasks)(nil)
