package health

import (
	"context"
	"errors"
	"github.com/nmhossain02/mailman/internal/adapters/keyring"
	"github.com/nmhossain02/mailman/internal/agent"
	"testing"
)

type ping func(context.Context) error

func (p ping) Ping(ctx context.Context) error { return p(ctx) }
func TestRunDiagnosesDependencies(t *testing.T) {
	checks := Run(context.Background(), Inputs{Database: ping(func(context.Context) error { return nil }), Secrets: keyring.NewMemoryStore(), LocalModel: &agent.FakeBackend{BackendID: "local", HealthFunc: func(context.Context) agent.Health { return agent.Health{Ready: true, ModelRevision: "digest"} }}})
	if err := Healthy(checks); err != nil {
		t.Fatal(err)
	}
	checks = Run(context.Background(), Inputs{Database: ping(func(context.Context) error { return errors.New("locked") })})
	if Healthy(checks) == nil {
		t.Fatal("unhealthy checks accepted")
	}
}
