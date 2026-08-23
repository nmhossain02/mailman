package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nabeel/mailman/internal/core"
)

const (
	RouteLocalOnly    = "local_only"
	RouteExternalOnly = "external_only"
	RouteFallback     = "fallback"
	RouteProbe        = "probe"
	RouteOracle       = "oracle"
)

type Observation struct {
	BackendClass string          `json:"backend_class"`
	Output       json.RawMessage `json:"output,omitempty"`
	Outcome      string          `json:"outcome"`
	ErrorKind    string          `json:"error_kind,omitempty"`
	InputTokens  *int64          `json:"input_tokens,omitempty"`
	CachedTokens *int64          `json:"cached_input_tokens,omitempty"`
	OutputTokens *int64          `json:"output_tokens,omitempty"`
	WallMS       *int64          `json:"wall_ms,omitempty"`
	GenerationMS *int64          `json:"generation_ms,omitempty"`
}

func (o Observation) valid() bool { return o.Outcome == "ok" && json.Valid(o.Output) }

type ExecuteFunc func(context.Context, core.EvalCase, string) Observation

type CaseResult struct {
	CaseID        string          `json:"case_id"`
	TaskName      string          `json:"task_name"`
	ExpectedJSON  json.RawMessage `json:"expected_json,omitempty"`
	Local         *Observation    `json:"local,omitempty"`
	External      *Observation    `json:"external,omitempty"`
	Selected      string          `json:"selected"`
	SelectedGrade *Grade          `json:"selected_grade,omitempty"`
	LocalGrade    *Grade          `json:"local_grade,omitempty"`
	ExternalGrade *Grade          `json:"external_grade,omitempty"`
	Disagrees     bool            `json:"disagrees,omitempty"`
	Escalated     bool            `json:"escalated,omitempty"`
	PairedProbe   bool            `json:"paired_probe,omitempty"`
}

type RunResult struct {
	Config  core.EvalRunConfig `json:"config"`
	Cases   []CaseResult       `json:"cases"`
	Metrics Metrics            `json:"metrics"`
}

func Snapshot(id, dataset, mode string) core.EvalRunConfig {
	return core.EvalRunConfig{ID: id, Dataset: dataset, RouteMode: mode, Concurrency: 1, CacheEnabled: false}
}

func FiveSnapshots(dataset string) []core.EvalRunConfig {
	modes := []string{RouteLocalOnly, RouteExternalOnly, RouteFallback, RouteProbe, RouteOracle}
	out := make([]core.EvalRunConfig, 0, len(modes))
	for _, mode := range modes {
		out = append(out, Snapshot(dataset+"-"+mode, dataset, mode))
	}
	return out
}

func Run(ctx context.Context, config core.EvalRunConfig, records []DatasetRecord, execute ExecuteFunc) (RunResult, error) {
	if execute == nil {
		return RunResult{}, errors.New("eval executor is required")
	}
	if config.Concurrency < 1 {
		return RunResult{}, errors.New("eval concurrency must be positive")
	}
	switch config.RouteMode {
	case RouteLocalOnly, RouteExternalOnly, RouteFallback, RouteProbe, RouteOracle:
	default:
		return RunResult{}, fmt.Errorf("unsupported eval route %q", config.RouteMode)
	}
	ordered := append([]DatasetRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Case.ID < ordered[j].Case.ID })
	result := RunResult{Config: config}
	externalCalls := 0
	for _, record := range ordered {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}
		caseResult := CaseResult{CaseID: record.Case.ID, TaskName: record.Case.TaskName, ExpectedJSON: record.ExpectedJSON}
		runLocal := func() {
			value := execute(ctx, record.Case, "local")
			value.BackendClass = "local"
			caseResult.Local = &value
		}
		runExternal := func() bool {
			if externalCalls >= config.MaxExternalCalls {
				return false
			}
			value := execute(ctx, record.Case, "external")
			value.BackendClass = "external"
			caseResult.External = &value
			externalCalls++
			return true
		}
		switch config.RouteMode {
		case RouteLocalOnly:
			runLocal()
			caseResult.Selected = "local"
		case RouteExternalOnly:
			if !runExternal() {
				return RunResult{}, errors.New("external call cap prevents external-only run")
			}
			caseResult.Selected = "external"
		case RouteFallback:
			runLocal()
			caseResult.Selected = "local"
			if !caseResult.Local.valid() && runExternal() {
				caseResult.Selected = "external"
				caseResult.Escalated = true
			}
		case RouteProbe:
			runLocal()
			if !runExternal() {
				return RunResult{}, errors.New("external call cap prevents paired probe run")
			}
			caseResult.Selected = "local"
			caseResult.PairedProbe = true
		case RouteOracle:
			runLocal()
			if !runExternal() {
				return RunResult{}, errors.New("external call cap prevents paired oracle run")
			}
			caseResult.Selected = "local"
		}
		gradeCase(&caseResult)
		if config.RouteMode == RouteOracle && caseResult.ExternalGrade != nil && caseResult.LocalGrade != nil && caseResult.ExternalGrade.Score > caseResult.LocalGrade.Score {
			caseResult.Selected = "external"
			caseResult.SelectedGrade = caseResult.ExternalGrade
		}
		result.Cases = append(result.Cases, caseResult)
	}
	pricing, err := ParsePricing(config.Pricing)
	if err != nil {
		return RunResult{}, err
	}
	result.Metrics = ComputeMetrics(result.Cases, pricing)
	return result, nil
}

func gradeCase(result *CaseResult) {
	if result.Local != nil && result.External != nil && result.Local.valid() && result.External.valid() {
		equal, err := canonicalEqual(result.Local.Output, result.External.Output)
		result.Disagrees = err != nil || !equal
	}
	if len(result.ExpectedJSON) == 0 {
		return
	}
	if result.Local != nil && result.Local.valid() {
		if grade, err := GradeOutput(result.TaskName, result.ExpectedJSON, result.Local.Output); err == nil {
			result.LocalGrade = &grade
		}
	}
	if result.External != nil && result.External.valid() {
		if grade, err := GradeOutput(result.TaskName, result.ExpectedJSON, result.External.Output); err == nil {
			result.ExternalGrade = &grade
		}
	}
	if result.Selected == "local" {
		result.SelectedGrade = result.LocalGrade
	} else {
		result.SelectedGrade = result.ExternalGrade
	}
}

func canonicalEqual(a, b json.RawMessage) (bool, error) {
	var left, right any
	if err := decodeJSON(a, &left); err != nil {
		return false, err
	}
	if err := decodeJSON(b, &right); err != nil {
		return false, err
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON), nil
}

// FrozenAt records when a caller deliberately created a snapshot; Run never
// changes this value or performs a pricing lookup.
func FrozenAt(date time.Time) string { return date.UTC().Format("2006-01-02") }
