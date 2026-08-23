package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Validator interface{ Validate() error }

// Decoder is deliberately non-generic at the boundary so tasks can be stored
// in registries while still decoding into task-specific concrete structs.
type Decoder func(json.RawMessage) (any, error)

type Task struct {
	Name, Version, PromptVersion, SchemaVersion string
	Instructions, Model                         string
	Schema                                      json.RawMessage
	MaxOutputTokens                             int
	Decode                                      Decoder
}

func RunTask(ctx context.Context, backend Backend, task Task, input json.RawMessage, traceID string) (TaskResult, error) {
	if backend == nil || task.Decode == nil {
		return TaskResult{}, errors.New("inference task is not configured")
	}
	raw, err := backend.Infer(ctx, Request{
		TaskName: task.Name, TaskVersion: task.Version, PromptVersion: task.PromptVersion,
		SchemaVersion: task.SchemaVersion, Instructions: task.Instructions, Model: task.Model,
		InputJSON: input, OutputSchema: task.Schema, MaxOutputTokens: task.MaxOutputTokens, TraceID: traceID,
	})
	if err != nil {
		return TaskResult{}, err
	}
	if err := validateRequired(raw.RawOutput, task.Schema); err != nil {
		return TaskResult{Raw: raw}, &InferenceError{Kind: "invalid_output", SafeMessage: err.Error()}
	}
	output, err := task.Decode(raw.RawOutput)
	if err != nil {
		return TaskResult{Raw: raw}, &InferenceError{Kind: "invalid_output", SafeMessage: err.Error()}
	}
	outcome := "ok"
	if abstains, ok := output.(interface{ IsAbstention() bool }); ok && abstains.IsAbstention() {
		outcome = "abstain"
	}
	return TaskResult{Outcome: outcome, Output: output, Raw: raw}, nil
}

// validateRequired enforces the small schema subset used by built-in tasks.
// This closes encoding/json's otherwise important missing-vs-zero-value gap.
func validateRequired(output, schema json.RawMessage) error {
	var value any
	var definition map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		return fmt.Errorf("decode output: %w", err)
	}
	if err := json.Unmarshal(schema, &definition); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	return walkRequired(value, definition, "output")
}

func walkRequired(value any, schema map[string]any, path string) error {
	if required, ok := schema["required"].([]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, rawName := range required {
			name, _ := rawName.(string)
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s missing required field %q", path, name)
			}
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name, childValue := range object {
				childSchema, ok := properties[name].(map[string]any)
				if !ok {
					continue
				}
				if err := walkRequired(childValue, childSchema, path+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if values, ok := value.([]any); ok {
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range values {
				if err := walkRequired(item, itemSchema, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func StrictDecoder[T Validator]() Decoder {
	return func(data json.RawMessage) (any, error) {
		var value T
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode output: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, errors.New("decode output: trailing JSON")
			}
			return nil, fmt.Errorf("decode output: %w", err)
		}
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("validate output: %w", err)
		}
		return value, nil
	}
}
