package eval

import (
	"encoding/json"
	"fmt"
	"math"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// gateSpec is one entry of a suite's gate_json.
type gateSpec struct {
	Metric string `json:"metric"`
	// "non_inferior" | ">=" | "abs>=" | "<=".
	Op string `json:"op"`
	// Non-inferiority / tolerance margin, or the absolute floor/ceiling for
	// abs>= / <=.
	Margin float64 `json:"margin,omitempty"`
	// Confidence for the non_inferior Wilson bound (default 0.95).
	Confidence float64 `json:"confidence,omitempty"`
}

// defaultGate is used when a suite declares no gate_json: the candidate's
// success rate must be non-inferior to the baseline within 3 points.
var defaultGate = []gateSpec{{Metric: "success_rate", Op: "non_inferior", Margin: 0.03, Confidence: 0.95}}

// evaluateGate compares a run's metrics against a baseline under the suite gate
// policy. minScored is the minimum PASS+FAIL required for a rate decision; below
// it the gate is inconclusive (treated as fail in CI). A nil baseline means no
// champion yet (bootstrap): baseline-relative checks pass with a noted reason so
// the first qualifying candidate can seed the baseline via PromoteBaseline.
func evaluateGate(m runMetrics, baseline *gantryv1.Baseline, gateJSON string, minScored int) (*gantryv1.GateResult, error) {
	specs, err := parseGateSpecs(gateJSON)
	if err != nil {
		return nil, err
	}
	res := &gantryv1.GateResult{
		Pass: uint32(m.Pass),
		Fail: uint32(m.Fail),
		Void: uint32(m.Void),
	}
	if baseline != nil {
		res.Baseline = baseline.Subject
	}
	passed, inconclusive := true, false
	for _, spec := range specs {
		gc := evalGateCheck(spec, m, baseline, minScored)
		res.Checks = append(res.Checks, gc)
		if !gc.Passed {
			passed = false
		}
		if isInconclusive(gc) {
			inconclusive = true
		}
	}
	res.Inconclusive = inconclusive
	res.Passed = passed && !inconclusive
	return res, nil
}

func evalGateCheck(spec gateSpec, m runMetrics, baseline *gantryv1.Baseline, minScored int) *gantryv1.GateCheck {
	cand, ok := m.Values[spec.Metric]
	gc := &gantryv1.GateCheck{Metric: spec.Metric, CandidateValue: cand, Op: spec.Op, Margin: spec.Margin}
	if !ok {
		gc.Detail = fmt.Sprintf("metric %q not computed", spec.Metric)
		return gc
	}

	switch spec.Op {
	case "abs>=":
		gc.Passed = cand >= spec.Margin
		gc.Detail = fmt.Sprintf("%.4f >= floor %.4f", cand, spec.Margin)
	case "<=":
		gc.Passed = cand <= spec.Margin
		gc.Detail = fmt.Sprintf("%.4f <= ceiling %.4f", cand, spec.Margin)
	case ">=", "non_inferior":
		if baseline == nil {
			gc.Passed = true
			gc.Detail = "no baseline yet (bootstrap): candidate seeds the champion"
			return gc
		}
		base := baselineMetric(spec.Metric, baseline)
		gc.BaselineValue = base
		if spec.Op == ">=" {
			gc.Passed = cand >= base-spec.Margin
			gc.Detail = fmt.Sprintf("%.4f >= baseline %.4f - margin %.4f", cand, base, spec.Margin)
			return gc
		}
		// non_inferior: Wilson lower bound of the candidate rate must clear the
		// baseline minus the non-inferiority margin. Requires trial counts.
		if m.scored() < minScored {
			gc.Detail = fmt.Sprintf("inconclusive: %d scored < %d required", m.scored(), minScored)
			return gc
		}
		z := zFor(spec.Confidence)
		lower := wilsonLower(m.Pass, m.scored(), z)
		threshold := base - spec.Margin
		gc.Passed = lower >= threshold
		gc.Detail = fmt.Sprintf("Wilson %.0f%% lower bound %.4f %s baseline %.4f - margin %.4f = %.4f",
			confPct(spec.Confidence), lower, cmp(gc.Passed), base, spec.Margin, threshold)
	default:
		gc.Detail = fmt.Sprintf("unknown op %q", spec.Op)
	}
	return gc
}

// baselineMetric resolves a baseline's value for a metric. Only success_rate is
// stored on the baseline in M1; other metrics fall back to 0 (so pair them with
// abs>= / <= absolute checks until per-metric baselines land).
func baselineMetric(metric string, b *gantryv1.Baseline) float64 {
	if metric == "success_rate" {
		return b.SuccessRate
	}
	return 0
}

func isInconclusive(gc *gantryv1.GateCheck) bool {
	return !gc.Passed && len(gc.Detail) >= 12 && gc.Detail[:12] == "inconclusive"
}

// wilsonLower returns the lower bound of the Wilson score interval for k
// successes in n trials at z standard deviations. n == 0 yields 0.
func wilsonLower(k, n int, z float64) float64 {
	if n == 0 {
		return 0
	}
	pHat := float64(k) / float64(n)
	nf := float64(n)
	denom := 1 + z*z/nf
	center := pHat + z*z/(2*nf)
	margin := z * math.Sqrt(pHat*(1-pHat)/nf+z*z/(4*nf*nf))
	lower := (center - margin) / denom
	if lower < 0 {
		return 0
	}
	return lower
}

// zFor maps a confidence level to a two-sided normal z for the one-sided Wilson
// bound (default 0.95 -> 1.95996).
func zFor(confidence float64) float64 {
	switch {
	case confidence >= 0.99:
		return 2.575829
	case confidence >= 0.975:
		return 2.241403
	case confidence >= 0.95, confidence == 0:
		return 1.959964
	case confidence >= 0.90:
		return 1.644854
	default:
		return 1.959964
	}
}

func confPct(c float64) float64 {
	if c == 0 {
		return 95
	}
	return c * 100
}

func cmp(pass bool) string {
	if pass {
		return ">="
	}
	return "<"
}

func parseGateSpecs(s string) ([]gateSpec, error) {
	if s == "" {
		return defaultGate, nil
	}
	var specs []gateSpec
	if err := json.Unmarshal([]byte(s), &specs); err != nil {
		return nil, fmt.Errorf("%w: gate_json: %v", ErrInvalid, err)
	}
	if len(specs) == 0 {
		return defaultGate, nil
	}
	return specs, nil
}
