package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// runMetrics is the aggregate of a run's scored trials. success_rate counts
// PASS / (PASS + FAIL) with VOID excluded, so bench-staging mistakes never move
// the number. Values holds success_rate plus any metrics the suite requested.
type runMetrics struct {
	Pass, Fail, Void int
	Values           map[string]float64
}

// scored is the denominator for the success rate (VOID excluded).
func (m runMetrics) scored() int { return m.Pass + m.Fail }

// successRate is PASS / (PASS + FAIL), or 0 when nothing scored.
func (m runMetrics) successRate() float64 {
	if m.scored() == 0 {
		return 0
	}
	return float64(m.Pass) / float64(m.scored())
}

// voidRate is VOID / (PASS + FAIL + VOID), or 0 when the run is empty. Unlike
// success_rate, VOID is the numerator here: a high void_rate flags a flaky bench
// (bad staging, dropped telemetry) rather than a bad candidate, so a suite can
// gate on it with a `<=` ceiling (finding #9a).
func (m runMetrics) voidRate() float64 {
	total := m.Pass + m.Fail + m.Void
	if total == 0 {
		return 0
	}
	return float64(m.Void) / float64(total)
}

// metricSpec is one entry of a suite's metrics_json: a named aggregate over a
// per-trial numeric check value.
type metricSpec struct {
	Name  string `json:"name"`
	Check string `json:"check"`         // per-trial numeric metric name (from a NUMERIC check)
	Agg   string `json:"agg,omitempty"` // p50 | mean | max | min (default p50)
}

// aggregate tallies trial dispositions and computes the metric values. It always
// emits "success_rate"; metricsJSON (may be empty) adds numeric aggregates.
func aggregate(trials []*gantryv1.Trial, metricsJSON string) (runMetrics, error) {
	m := runMetrics{Values: map[string]float64{}}
	perMetric := map[string][]float64{}
	for _, t := range trials {
		if t.Outcome == nil {
			continue
		}
		switch t.Outcome.Disposition {
		case gantryv1.Disposition_DISPOSITION_PASS:
			m.Pass++
		case gantryv1.Disposition_DISPOSITION_FAIL:
			m.Fail++
		case gantryv1.Disposition_DISPOSITION_VOID:
			m.Void++
		}
		for name, v := range t.Outcome.Metrics {
			perMetric[name] = append(perMetric[name], v)
		}
	}
	m.Values["success_rate"] = m.successRate()
	m.Values["void_rate"] = m.voidRate()

	specs, err := parseMetricSpecs(metricsJSON)
	if err != nil {
		return m, err
	}
	for _, spec := range specs {
		vals := perMetric[spec.Check]
		if len(vals) == 0 {
			continue
		}
		m.Values[spec.Name] = reduce(vals, spec.Agg)
	}
	return m, nil
}

func parseMetricSpecs(s string) ([]metricSpec, error) {
	if s == "" {
		return nil, nil
	}
	var specs []metricSpec
	if err := json.Unmarshal([]byte(s), &specs); err != nil {
		return nil, fmt.Errorf("%w: metrics_json: %v", ErrInvalid, err)
	}
	return specs, nil
}

// reduce collapses samples by the named aggregate (default p50).
func reduce(vals []float64, agg string) float64 {
	switch agg {
	case "mean":
		var sum float64
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	case "max":
		return slicesMax(vals)
	case "min":
		return slicesMin(vals)
	default: // p50 / median
		return percentile(vals, 50)
	}
}

// percentile returns the p-th percentile (0..100) via linear interpolation.
func percentile(vals []float64, p float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	rank := (p / 100) * float64(len(s)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return s[lo]
	}
	frac := rank - float64(lo)
	return s[lo]*(1-frac) + s[hi]*frac
}

func slicesMax(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func slicesMin(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
