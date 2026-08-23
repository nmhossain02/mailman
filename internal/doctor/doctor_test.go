package doctor

import (
	"context"
	"errors"
	"github.com/nmhossain02/mailman/internal/inference"
	"github.com/nmhossain02/mailman/internal/secret"
	"testing"
)

type ping func(context.Context) error

func (p ping) Ping(ctx context.Context) error { return p(ctx) }
func TestRunDiagnosesDependencies(t *testing.T) {
	checks := Run(context.Background(), Inputs{Database: ping(func(context.Context) error { return nil }), Secrets: secret.NewMemoryStore(), LocalModel: &inference.FakeBackend{BackendID: "local", HealthFunc: func(context.Context) inference.Health { return inference.Health{Ready: true, ModelRevision: "digest"} }}})
	if err := Healthy(checks); err != nil {
		t.Fatal(err)
	}
	checks = Run(context.Background(), Inputs{Database: ping(func(context.Context) error { return errors.New("locked") })})
	if Healthy(checks) == nil {
		t.Fatal("unhealthy checks accepted")
	}
}
