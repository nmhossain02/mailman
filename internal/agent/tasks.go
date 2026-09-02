package agent

import (
	"embed"
	"encoding/json"
	"fmt"

	core "github.com/nmhossain02/mailman/internal/domain"
)

//go:embed prompts/*.txt prompts/*.json
var promptFiles embed.FS

type MessageKindResult core.MessageKindOutput

func (r MessageKindResult) Validate() error    { return core.MessageKindOutput(r).Validate() }
func (r MessageKindResult) IsAbstention() bool { return r.Abstain }

type RequestsDatesResult core.RequestsDatesOutput

func (r RequestsDatesResult) Validate() error    { return core.RequestsDatesOutput(r).Validate() }
func (r RequestsDatesResult) IsAbstention() bool { return r.Abstain }

type SummaryDeltaResult core.SummaryDeltaOutput

func (r SummaryDeltaResult) Validate() error    { return core.SummaryDeltaOutput(r).Validate() }
func (r SummaryDeltaResult) IsAbstention() bool { return r.Abstain }

type CommandResult core.CommandDraft

func (r CommandResult) Validate() error { return core.CommandDraft(r).Validate() }

func BuiltinTask(name, model string) (Task, error) {
	type taskSpec struct {
		version, max string
		tokens       int
		decode       Decoder
	}
	specs := map[string]taskSpec{
		"message_kind":      {"1", "message_kind", 256, StrictDecoder[MessageKindResult]()},
		"requests_dates":    {"1", "requests_dates", 768, StrictDecoder[RequestsDatesResult]()},
		"summary_delta":     {"1", "summary_delta", 768, StrictDecoder[SummaryDeltaResult]()},
		"translate_command": {"1", "translate_command", 768, StrictDecoder[CommandResult]()},
	}
	spec, ok := specs[name]
	if !ok {
		return Task{}, fmt.Errorf("unknown inference task %q", name)
	}
	instructions, err := promptFiles.ReadFile("prompts/" + spec.max + ".txt")
	if err != nil {
		return Task{}, err
	}
	schema, err := promptFiles.ReadFile("prompts/" + spec.max + ".json")
	if err != nil {
		return Task{}, err
	}
	if !json.Valid(schema) {
		return Task{}, fmt.Errorf("invalid embedded schema for %s", name)
	}
	return Task{Name: name, Version: spec.version, PromptVersion: "1", SchemaVersion: "1", Instructions: string(instructions), Model: model, Schema: schema, MaxOutputTokens: spec.tokens, Decode: spec.decode}, nil
}
