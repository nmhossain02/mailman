package bootstrap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nmhossain02/mailman/internal/application/progress"
)

func TestSyncProgressReporterSamplesItemUpdates(t *testing.T) {
	var output bytes.Buffer
	report := syncProgressReporter(&output, "personal")
	for _, event := range []progress.Event{
		{Stage: progress.StageStarting},
		{Stage: progress.StageFetchingPage, Current: 1},
		{Stage: progress.StageMetadata, Current: 1, Total: 120},
		{Stage: progress.StageMetadata, Current: 2, Total: 120},
		{Stage: progress.StageMetadata, Current: 50, Total: 120},
		{Stage: progress.StageMetadata, Current: 120, Total: 120},
		{Stage: progress.StagePageCommitted, Pages: 1, Messages: 120},
		{Stage: progress.StageDone, Pages: 1, Messages: 120, Conversations: 40},
	} {
		report(event)
	}
	got := output.String()
	for _, wanted := range []string{"starting full sync", "fetching page 1", "metadata 1/120", "metadata 50/120", "metadata 120/120", "committed page 1", "complete (1 pages, 120 messages, 40 conversations)"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("progress output missing %q:\n%s", wanted, got)
		}
	}
	if strings.Contains(got, "metadata 2/120") {
		t.Fatalf("progress output was not sampled:\n%s", got)
	}
}
