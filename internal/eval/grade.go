package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/nmhossain02/mailman/internal/core"
)

type Grade struct {
	Exact         bool    `json:"exact"`
	MatchedFields int     `json:"matched_fields"`
	TotalFields   int     `json:"total_fields"`
	Score         float64 `json:"score"`
}

func GradeOutput(taskName string, expected, actual json.RawMessage) (Grade, error) {
	if taskName == "natural_command" || taskName == "command_translation" {
		return gradeCommand(expected, actual)
	}
	var want, got any
	if err := decodeJSON(expected, &want); err != nil {
		return Grade{}, fmt.Errorf("decode expected output: %w", err)
	}
	if err := decodeJSON(actual, &got); err != nil {
		return Grade{}, fmt.Errorf("decode actual output: %w", err)
	}
	matched, total := compareFields(want, got)
	score := 1.0
	if total > 0 {
		score = float64(matched) / float64(total)
	}
	return Grade{Exact: reflect.DeepEqual(want, got), MatchedFields: matched, TotalFields: total, Score: score}, nil
}

func gradeCommand(expected, actual json.RawMessage) (Grade, error) {
	var want, got core.CommandDraft
	if err := decodeJSON(expected, &want); err != nil {
		return Grade{}, fmt.Errorf("decode expected command: %w", err)
	}
	if err := decodeJSON(actual, &got); err != nil {
		return Grade{}, fmt.Errorf("decode actual command: %w", err)
	}
	canonicalizeCommand(&want)
	canonicalizeCommand(&got)
	exact := reflect.DeepEqual(want, got)
	matched := 0
	if exact {
		matched = 1
	}
	return Grade{Exact: exact, MatchedFields: matched, TotalFields: 1, Score: float64(matched)}, nil
}

func canonicalizeCommand(command *core.CommandDraft) {
	if command.Filters == nil {
		command.Filters = []core.Filter{}
	}
	if command.Actions == nil {
		command.Actions = []core.Action{}
	}
	sort.Slice(command.Filters, func(i, j int) bool {
		a, b := command.Filters[i], command.Filters[j]
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Operator != b.Operator {
			return a.Operator < b.Operator
		}
		return a.Value < b.Value
	})
	sort.Slice(command.Actions, func(i, j int) bool {
		a, b := command.Actions[i], command.Actions[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Argument < b.Argument
	})
}

func decodeJSON(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	return requireEOF(dec)
}

func compareFields(want, got any) (matched, total int) {
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		for key, value := range expected {
			if !ok {
				_, fields := compareFields(value, nil)
				total += fields
				continue
			}
			m, fields := compareFields(value, actual[key])
			matched += m
			total += fields
		}
		return matched, total
	case []any:
		actual, ok := got.([]any)
		if len(expected) == 0 {
			if ok && len(actual) == 0 {
				return 1, 1
			}
			return 0, 1
		}
		for i, value := range expected {
			var actualValue any
			if ok && i < len(actual) {
				actualValue = actual[i]
			}
			m, fields := compareFields(value, actualValue)
			matched += m
			total += fields
		}
		return matched, total
	default:
		if reflect.DeepEqual(want, got) {
			return 1, 1
		}
		return 0, 1
	}
}
