package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

func WriteJSON(w io.Writer, result RunResult) error {
	copyResult := result
	copyResult.Cases = append([]CaseResult(nil), result.Cases...)
	sort.SliceStable(copyResult.Cases, func(i, j int) bool { return copyResult.Cases[i].CaseID < copyResult.Cases[j].CaseID })
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(copyResult)
}

func WriteTable(w io.Writer, result RunResult) error {
	rows := append([]CaseResult(nil), result.Cases...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CaseID < rows[j].CaseID })
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CASE\tSELECTED\tOUTCOME\tSCORE"); err != nil {
		return err
	}
	for _, row := range rows {
		selected := row.Local
		if row.Selected == "external" {
			selected = row.External
		}
		outcome := "not-run"
		if selected != nil {
			outcome = selected.Outcome
		}
		score := "N/A"
		if row.SelectedGrade != nil {
			score = fmt.Sprintf("%.3f", row.SelectedGrade.Score)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.CaseID, row.Selected, outcome, score); err != nil {
			return err
		}
	}
	metrics := []struct {
		name  string
		value *float64
	}{
		{"valid output", result.Metrics.ValidOutputRate}, {"abstention", result.Metrics.AbstentionRate},
		{"escalation", result.Metrics.EscalationRate}, {"local resolution", result.Metrics.LocalResolutionRate},
		{"external lift", result.Metrics.ExternalLift}, {"avoidable escalation", result.Metrics.AvoidableEscalationRate},
		{"missed escalation", result.Metrics.MissedEscalationRate}, {"routing regret", result.Metrics.RoutingRegret},
		{"probe disagreement", result.Metrics.ProbeDisagreementRate},
	}
	if _, err := fmt.Fprintln(tw, "\nMETRIC\tVALUE"); err != nil {
		return err
	}
	for _, metric := range metrics {
		value := "N/A"
		if metric.value != nil {
			value = fmt.Sprintf("%.3f", *metric.value)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", metric.name, value); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(tw, "local usage\t%d in / %d out / $%.6f\n", result.Metrics.Local.InputTokens, result.Metrics.Local.OutputTokens, result.Metrics.Local.Cost); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(tw, "external usage\t%d in / %d out / $%.6f\n", result.Metrics.External.InputTokens, result.Metrics.External.OutputTokens, result.Metrics.External.Cost); err != nil {
		return err
	}
	return tw.Flush()
}
