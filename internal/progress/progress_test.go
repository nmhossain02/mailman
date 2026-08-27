package progress

import (
	"context"
	"testing"
)

func TestReporterIsOptionalAndScopedToContext(t *testing.T) {
	Report(context.Background(), Event{Stage: StageStarting})
	var got Event
	ctx := WithReporter(context.Background(), func(event Event) { got = event })
	Report(ctx, Event{Stage: StageMetadata, Current: 2, Total: 3})
	if got.Stage != StageMetadata || got.Current != 2 || got.Total != 3 {
		t.Fatalf("unexpected event: %+v", got)
	}
}
