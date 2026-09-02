package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nmhossain02/mailman/internal/agent"
)

func TestCanonicalCommandEquality(t *testing.T) {
	expected := json.RawMessage(`{"intent":"propose","target":"conversation","filters":[{"Field":"read","Operator":"eq","Value":"false"},{"Field":"kind","Operator":"eq","Value":"newsletter"}],"actions":[{"Kind":"mark_read","Argument":""},{"Kind":"archive","Argument":""}]}`)
	actual := json.RawMessage(`{"intent":"propose","target":"conversation","filters":[{"Field":"kind","Operator":"eq","Value":"newsletter"},{"Field":"read","Operator":"eq","Value":"false"}],"actions":[{"Kind":"archive","Argument":""},{"Kind":"mark_read","Argument":""}]}`)
	grade, err := GradeOutput("natural_command", expected, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !grade.Exact || grade.Score != 1 {
		t.Fatalf("grade = %+v", grade)
	}
}

func TestPartialFieldScore(t *testing.T) {
	grade, err := GradeOutput("message_kind", json.RawMessage(`{"kind":"newsletter","abstain":false,"evidence":["m1"]}`), json.RawMessage(`{"kind":"alert","abstain":false,"evidence":["m1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if grade.Exact || grade.MatchedFields != 2 || grade.TotalFields != 3 || grade.Score != 2.0/3.0 {
		t.Fatalf("grade = %+v", grade)
	}
}

func TestJSONLRoundTripAndHashValidation(t *testing.T) {
	records := []DatasetRecord{{Case: agent.EvalCase{ID: "b", Dataset: "mail", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{"subject":"B"}`)}}, {Case: agent.EvalCase{ID: "a", Dataset: "mail", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{"subject":"A"}`)}, InputReference: "fixture:a", ExpectedJSON: json.RawMessage(`{"kind":"work"}`)}}
	var output bytes.Buffer
	if err := WriteJSONL(&output, records); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSONL(&output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Case.ID != "a" || got[0].Case.InputHash == "" {
		t.Fatalf("records = %+v", got)
	}
	bad := `{"case":{"ID":"bad","Dataset":"mail","TaskName":"message_kind","TaskVersion":"1","InputJSON":{},"InputHash":"` + strings.Repeat("0", 64) + `"}}`
	if _, err := ReadJSONL(strings.NewReader(bad)); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestCheckedInDatasetAndFiveSnapshots(t *testing.T) {
	file, err := os.Open("../../../testdata/eval/message_kind.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := ReadJSONL(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].InputReference == "" || len(records[0].ExpectedJSON) == 0 {
		t.Fatalf("records = %+v", records)
	}
	snapshots := FiveSnapshots("starter")
	if len(snapshots) != 5 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	for _, snapshot := range snapshots {
		if snapshot.CacheEnabled || snapshot.Concurrency != 1 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	}
}

func TestZeroDenominatorMetricsAreUnavailable(t *testing.T) {
	metrics := ComputeMetrics(nil, Pricing{})
	if metrics.ValidOutputRate != nil || metrics.ExternalLift != nil || metrics.ProbeDisagreementRate != nil {
		t.Fatalf("metrics = %+v", metrics)
	}
	var report bytes.Buffer
	if err := WriteTable(&report, RunResult{Metrics: metrics}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "N/A") {
		t.Fatalf("report = %s", report.String())
	}
}

func TestProbeGroupsPairedOutputsAndKeepsLocal(t *testing.T) {
	records := []DatasetRecord{{Case: agent.EvalCase{ID: "c1", Dataset: "d", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{}`)}}}
	config := Snapshot("run", "d", RouteProbe)
	config.MaxExternalCalls = 1
	result, err := Run(context.Background(), config, records, func(_ context.Context, _ agent.EvalCase, backend string) Observation {
		return Observation{Outcome: "ok", Output: json.RawMessage(`{"backend":"` + backend + `"}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Cases[0]
	if got.Local == nil || got.External == nil || got.Selected != "local" || !got.Disagrees {
		t.Fatalf("result = %+v", got)
	}
	if result.Metrics.ExternalLift != nil {
		t.Fatal("unlabeled external lift must be unavailable")
	}
	if result.Metrics.ProbeDisagreementRate == nil || *result.Metrics.ProbeDisagreementRate != 1 {
		t.Fatalf("metrics = %+v", result.Metrics)
	}
}

func TestOracleSelectsHigherScoreAndBreaksTieLocal(t *testing.T) {
	records := []DatasetRecord{
		{Case: agent.EvalCase{ID: "better", Dataset: "d", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{}`)}, ExpectedJSON: json.RawMessage(`{"kind":"work","abstain":false}`)},
		{Case: agent.EvalCase{ID: "tie", Dataset: "d", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{}`)}, ExpectedJSON: json.RawMessage(`{"kind":"work","abstain":false}`)},
	}
	config := Snapshot("run", "d", RouteOracle)
	config.MaxExternalCalls = 2
	result, err := Run(context.Background(), config, records, func(_ context.Context, c agent.EvalCase, backend string) Observation {
		kind := "work"
		if c.ID == "better" && backend == "local" {
			kind = "alert"
		}
		return Observation{Outcome: "ok", Output: json.RawMessage(`{"kind":"` + kind + `","abstain":false}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cases[0].Selected != "external" || result.Cases[1].Selected != "local" {
		t.Fatalf("cases = %+v", result.Cases)
	}
}

func TestDeterministicReportOrdering(t *testing.T) {
	result := RunResult{Cases: []CaseResult{{CaseID: "z", Selected: "local"}, {CaseID: "a", Selected: "local"}}}
	var first, second bytes.Buffer
	if err := WriteJSON(&first, result); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&second, result); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() || strings.Index(first.String(), `"a"`) > strings.Index(first.String(), `"z"`) {
		t.Fatalf("report = %s", first.String())
	}
}

func TestExternalUsageAndCostRemainSeparate(t *testing.T) {
	in, cached, out, generation := int64(100), int64(10), int64(20), int64(1000)
	cases := []CaseResult{{CaseID: "c", Selected: "external", Local: &Observation{Outcome: "ok", Output: json.RawMessage(`{}`), InputTokens: &in, OutputTokens: &out}, External: &Observation{Outcome: "ok", Output: json.RawMessage(`{}`), InputTokens: &in, CachedTokens: &cached, OutputTokens: &out, GenerationMS: &generation}}}
	metrics := ComputeMetrics(cases, Pricing{Local: BackendPrice{InputPerMillion: 1}, External: BackendPrice{InputPerMillion: 2, CachedInputPerMillion: 1, OutputPerMillion: 4}})
	if metrics.Local.Calls != 1 || metrics.External.Calls != 1 || metrics.Local.Cost == metrics.External.Cost {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metrics.External.ProviderTokensPerSecond == nil || *metrics.External.ProviderTokensPerSecond != 20 {
		t.Fatalf("tps = %+v", metrics.External.ProviderTokensPerSecond)
	}
}

func TestExternalModesHonorPositiveCap(t *testing.T) {
	record := DatasetRecord{Case: agent.EvalCase{ID: "c", Dataset: "d", TaskName: "message_kind", TaskVersion: "1", InputJSON: json.RawMessage(`{}`)}}
	_, err := Run(context.Background(), Snapshot("run", "d", RouteExternalOnly), []DatasetRecord{record}, func(context.Context, agent.EvalCase, string) Observation { return Observation{} })
	if err == nil {
		t.Fatal("expected positive external cap requirement")
	}
}
