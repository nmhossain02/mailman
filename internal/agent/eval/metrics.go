package eval

import (
	"encoding/json"
	"errors"
	"fmt"
)

type BackendPrice struct {
	InputPerMillion       float64 `json:"input_per_million"`
	CachedInputPerMillion float64 `json:"cached_input_per_million"`
	OutputPerMillion      float64 `json:"output_per_million"`
}

type Pricing struct {
	Local    BackendPrice `json:"local"`
	External BackendPrice `json:"external"`
}

func ParsePricing(raw json.RawMessage) (Pricing, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Pricing{}, nil
	}
	var pricing Pricing
	if err := json.Unmarshal(raw, &pricing); err != nil {
		return Pricing{}, fmt.Errorf("decode frozen pricing: %w", err)
	}
	for _, value := range []float64{pricing.Local.InputPerMillion, pricing.Local.CachedInputPerMillion, pricing.Local.OutputPerMillion, pricing.External.InputPerMillion, pricing.External.CachedInputPerMillion, pricing.External.OutputPerMillion} {
		if value < 0 {
			return Pricing{}, errors.New("frozen pricing cannot be negative")
		}
	}
	return pricing, nil
}

type Usage struct {
	Calls                   int64    `json:"calls"`
	InputTokens             int64    `json:"input_tokens"`
	CachedInputTokens       int64    `json:"cached_input_tokens"`
	OutputTokens            int64    `json:"output_tokens"`
	Cost                    float64  `json:"estimated_cost"`
	ProviderTokensPerSecond *float64 `json:"provider_tokens_per_second,omitempty"`
	providerOutputTokens    int64
	providerGenerationMS    int64
}

type Metrics struct {
	Cases                    int      `json:"cases"`
	ValidOutputRate          *float64 `json:"valid_output_rate"`
	AbstentionRate           *float64 `json:"abstention_rate"`
	EscalationRate           *float64 `json:"escalation_rate"`
	TimeoutSchemaFailureRate *float64 `json:"timeout_schema_failure_rate"`
	LocalResolutionRate      *float64 `json:"local_resolution_rate"`
	ExternalLift             *float64 `json:"external_lift"`
	AvoidableEscalationRate  *float64 `json:"avoidable_escalation_rate"`
	MissedEscalationRate     *float64 `json:"missed_escalation_rate"`
	RoutingRegret            *float64 `json:"routing_regret"`
	ProbeDisagreementRate    *float64 `json:"probe_disagreement_rate"`
	TotalWallMS              int64    `json:"total_wall_ms"`
	Local                    Usage    `json:"local"`
	External                 Usage    `json:"external"`
}

func ComputeMetrics(cases []CaseResult, pricing Pricing) Metrics {
	m := Metrics{Cases: len(cases)}
	valid, abstain, escalation, failure, localResolution := 0, 0, 0, 0, 0
	pairedLabeled, avoidable, avoidableDenominator, missed, missedDenominator := 0, 0, 0, 0, 0
	lift, regret := 0.0, 0.0
	unlabeledPaired, disagreements := 0, 0
	for _, c := range cases {
		selected := c.Local
		if c.Selected == "external" {
			selected = c.External
		}
		if c.Escalated {
			escalation++
		}
		if selected != nil {
			if selected.valid() {
				valid++
			}
			if selected.Outcome == "abstain" {
				abstain++
			}
			if selected.ErrorKind == "timeout" || selected.ErrorKind == "invalid_output" || selected.ErrorKind == "schema" {
				failure++
			}
			if c.Selected == "local" && selected.valid() {
				localResolution++
			}
			if selected.WallMS != nil {
				m.TotalWallMS += *selected.WallMS
			}
		}
		accumulateUsage(&m.Local, c.Local, pricing.Local)
		accumulateUsage(&m.External, c.External, pricing.External)
		if c.LocalGrade != nil && c.ExternalGrade != nil {
			pairedLabeled++
			delta := c.ExternalGrade.Score - c.LocalGrade.Score
			lift += delta
			if c.Escalated {
				avoidableDenominator++
				if c.LocalGrade.Score >= c.ExternalGrade.Score {
					avoidable++
				}
			}
			if c.Selected == "local" {
				missedDenominator++
				if delta > 0 {
					missed++
				}
			}
			best := c.LocalGrade.Score
			if c.ExternalGrade.Score > best {
				best = c.ExternalGrade.Score
			}
			selectedScore := 0.0
			if c.SelectedGrade != nil {
				selectedScore = c.SelectedGrade.Score
			}
			regret += best - selectedScore
		} else if len(c.ExpectedJSON) == 0 && c.PairedProbe && c.Local != nil && c.External != nil {
			unlabeledPaired++
			if c.Disagrees {
				disagreements++
			}
		}
	}
	m.ValidOutputRate = ratio(valid, len(cases))
	m.AbstentionRate = ratio(abstain, len(cases))
	m.EscalationRate = ratio(escalation, len(cases))
	m.TimeoutSchemaFailureRate = ratio(failure, len(cases))
	m.LocalResolutionRate = ratio(localResolution, len(cases))
	if pairedLabeled > 0 {
		m.ExternalLift = number(lift / float64(pairedLabeled))
		m.AvoidableEscalationRate = ratio(avoidable, avoidableDenominator)
		m.MissedEscalationRate = ratio(missed, missedDenominator)
		m.RoutingRegret = number(regret / float64(pairedLabeled))
	}
	m.ProbeDisagreementRate = ratio(disagreements, unlabeledPaired)
	finishTPS(&m.Local)
	finishTPS(&m.External)
	return m
}

func accumulateUsage(usage *Usage, observation *Observation, price BackendPrice) {
	if observation == nil {
		return
	}
	usage.Calls++
	var input, cached, output int64
	if observation.InputTokens != nil {
		input = *observation.InputTokens
		usage.InputTokens += input
	}
	if observation.CachedTokens != nil {
		cached = *observation.CachedTokens
		usage.CachedInputTokens += cached
	}
	if observation.OutputTokens != nil {
		output = *observation.OutputTokens
		usage.OutputTokens += output
	}
	uncached := input - cached
	if uncached < 0 {
		uncached = 0
	}
	usage.Cost += (float64(uncached)*price.InputPerMillion + float64(cached)*price.CachedInputPerMillion + float64(output)*price.OutputPerMillion) / 1_000_000
	if observation.GenerationMS != nil && *observation.GenerationMS > 0 && output > 0 {
		usage.providerOutputTokens += output
		usage.providerGenerationMS += *observation.GenerationMS
	}
}

func finishTPS(usage *Usage) {
	if usage.providerGenerationMS > 0 {
		value := float64(usage.providerOutputTokens) * 1000 / float64(usage.providerGenerationMS)
		usage.ProviderTokensPerSecond = &value
	}
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	return number(float64(numerator) / float64(denominator))
}
func number(value float64) *float64 { return &value }
