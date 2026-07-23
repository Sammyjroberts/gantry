package eval

import (
	"context"
	"encoding/json"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Sampler computes a scalar aggregate of one telemetry channel over a device and
// time window. It is the seam between the eval engine and the telemetry store:
// core/go/eval never imports DuckDB or the segment reader; the hosting app wires
// a concrete Sampler (see the bench duckdbSampler). present is false when the
// window held no samples for the channel.
type Sampler interface {
	Aggregate(ctx context.Context, device, channel, col, agg string, startNs, endNs uint64) (value float64, present bool, err error)
}

// telemetryCheck is one check a telemetry verifier evaluates: aggregate `agg` of
// `channel`.`col` over the trial window, then pass iff (value op threshold).
type telemetryCheck struct {
	Name      string  `json:"name"`
	Phase     string  `json:"phase"` // precondition | during | outcome
	Required  bool    `json:"required"`
	Channel   string  `json:"channel"`
	Col       string  `json:"col,omitempty"` // v_f64 (default) | v_i64 | v_bool
	Agg       string  `json:"agg,omitempty"` // max (default) | min | avg | sum | count
	Op        string  `json:"op"`            // <= >= < > == !=
	Threshold float64 `json:"threshold"`
	Device    string  `json:"device,omitempty"` // overrides the trial's station id
}

// telemetryConfig is the "telemetry" block of a suite's verifier_config_json.
type telemetryConfig struct {
	VerifierID string           `json:"verifier_id,omitempty"`
	Version    string           `json:"version,omitempty"`
	Checks     []telemetryCheck `json:"checks"`
}

// verifierConfig is the top-level verifier_config_json. Only the telemetry
// verifier is interpreted in-core; vision/agent verifiers submit verdicts over
// the wire and need no server-side config here.
type verifierConfig struct {
	Telemetry *telemetryConfig `json:"telemetry"`
}

func parseVerifierConfig(s string) (*verifierConfig, error) {
	if s == "" {
		return &verifierConfig{}, nil
	}
	var cfg verifierConfig
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return nil, fmt.Errorf("%w: verifier_config_json: %v", ErrInvalid, err)
	}
	return &cfg, nil
}

// ScoreTrialTelemetry scores a closed trial with the suite's telemetry verifier
// (if one is configured and a Sampler is wired) and submits the resulting
// verdict, which recomputes the trial outcome. It is a no-op returning the trial
// unchanged when no Sampler is wired or the suite declares no telemetry checks —
// so it is safe to call unconditionally after CloseTrial.
func (s *Service) ScoreTrialTelemetry(ctx context.Context, trialID string) (*gantryv1.Trial, error) {
	t, err := s.store.GetTrial(ctx, trialID)
	if err != nil {
		return nil, err
	}
	if s.sampler == nil {
		return t, nil
	}
	run, err := s.store.GetRun(ctx, t.RunId)
	if err != nil {
		return nil, err
	}
	suite, err := s.store.GetSuite(ctx, run.SuiteId)
	if err != nil {
		return nil, err
	}
	cfg, err := parseVerifierConfig(suite.VerifierConfigJson)
	if err != nil {
		return nil, err
	}
	if cfg.Telemetry == nil || len(cfg.Telemetry.Checks) == 0 {
		return t, nil // no telemetry verifier for this suite
	}
	if t.EndedNs == 0 {
		return nil, fmt.Errorf("%w: trial %s not closed", ErrInvalid, trialID)
	}

	tc := cfg.Telemetry
	verdict := &gantryv1.Verdict{
		VerifierId:      orDefault(tc.VerifierID, "telemetry"),
		VerifierVersion: orDefault(tc.Version, "1"),
		ScoredFrom:      &gantryv1.ScoredEvidence{RangeStartNs: t.StartedNs, RangeEndNs: t.EndedNs},
	}
	for _, c := range tc.Checks {
		device := c.Device
		if device == "" {
			device = t.StationId
		}
		val, present, err := s.sampler.Aggregate(ctx, device, c.Channel, orDefault(c.Col, "v_f64"), orDefault(c.Agg, "max"), t.StartedNs, t.EndedNs)
		if err != nil {
			return nil, fmt.Errorf("eval: sample %q: %w", c.Channel, err)
		}
		chk := &gantryv1.Check{
			Name: c.Name, Phase: phaseOf(c.Phase), Required: c.Required,
			Kind: gantryv1.CheckKind_CHECK_KIND_NUMERIC, Op: c.Op, Threshold: c.Threshold,
			Value: val, EvidenceRefs: []string{"channel:" + c.Channel},
		}
		if !present {
			chk.Pass = false
			chk.Detail = "no telemetry samples in trial window"
		} else {
			chk.Pass = compare(val, c.Op, c.Threshold)
		}
		verdict.Checks = append(verdict.Checks, chk)
	}
	return s.SubmitVerdict(ctx, trialID, verdict)
}

func phaseOf(s string) gantryv1.Phase {
	switch s {
	case "precondition":
		return gantryv1.Phase_PHASE_PRECONDITION
	case "during":
		return gantryv1.Phase_PHASE_DURING
	case "outcome":
		return gantryv1.Phase_PHASE_OUTCOME
	default:
		return gantryv1.Phase_PHASE_OUTCOME
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
