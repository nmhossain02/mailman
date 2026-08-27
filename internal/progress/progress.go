package progress

import "context"

const (
	StageStarting      = "starting"
	StageFetchingPage  = "fetching-page"
	StageMetadata      = "metadata"
	StageContent       = "content"
	StagePageCommitted = "page-committed"
	StageRules         = "rules"
	StageDone          = "done"
)

type Event struct {
	Stage           string
	Current, Total  int
	Pages, Messages int
	Conversations   int
	Incremental     bool
}

type Reporter func(Event)

type reporterKey struct{}

func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, reporter)
}

func Report(ctx context.Context, event Event) {
	if reporter, ok := ctx.Value(reporterKey{}).(Reporter); ok {
		reporter(event)
	}
}
