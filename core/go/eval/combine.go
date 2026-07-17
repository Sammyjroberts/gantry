package eval

import (
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// deriveOutcome rolls a trial's verdicts (each a bundle of checks) into a single
// TrialOutcome under the default combine policy:
//
//	any required PRECONDITION check fails  -> VOID  (excluded from the success rate)
//	any required DURING check fails        -> FAIL  (interlock)
//	all required OUTCOME checks pass        -> PASS
//	otherwise                               -> FAIL
//
// A check named the same across verifiers is treated independently, so the
// effective cross-verifier rule for required checks is "all must pass". Numeric
// per-check values are surfaced as outcome metrics (e.g. task_time_s). When a
// trial has no checks at all the disposition is UNSPECIFIED (not yet scored).
// combine_json customisation (any/weighted) is a later milestone.
func deriveOutcome(verdicts []*gantryv1.Verdict) *gantryv1.TrialOutcome {
	out := &gantryv1.TrialOutcome{Metrics: map[string]float64{}}

	var (
		hasCheck                        bool
		precondFail, duringFail         *gantryv1.Check
		requiredOutcome, outcomePassing int
	)
	for _, v := range verdicts {
		for _, c := range v.Checks {
			hasCheck = true
			pass := evalCheck(c)
			if c.Kind == gantryv1.CheckKind_CHECK_KIND_NUMERIC && c.Name != "" {
				out.Metrics[c.Name] = c.Value
			}
			switch c.Phase {
			case gantryv1.Phase_PHASE_PRECONDITION:
				if c.Required && !pass && precondFail == nil {
					precondFail = c
				}
			case gantryv1.Phase_PHASE_DURING:
				if c.Required && !pass && duringFail == nil {
					duringFail = c
				}
			case gantryv1.Phase_PHASE_OUTCOME:
				if c.Required {
					requiredOutcome++
					if pass {
						outcomePassing++
					}
				}
			}
		}
	}

	switch {
	case !hasCheck:
		out.Disposition = gantryv1.Disposition_DISPOSITION_UNSPECIFIED
		out.Reason = "no checks submitted"
	case precondFail != nil:
		out.Disposition = gantryv1.Disposition_DISPOSITION_VOID
		out.Reason = fmt.Sprintf("precondition %q failed -> void", precondFail.Name)
	case duringFail != nil:
		out.Disposition = gantryv1.Disposition_DISPOSITION_FAIL
		out.Reason = fmt.Sprintf("interlock %q breached -> fail", duringFail.Name)
	case requiredOutcome > 0 && outcomePassing == requiredOutcome:
		out.Disposition = gantryv1.Disposition_DISPOSITION_PASS
		out.Reason = "all required outcome checks passed"
	case requiredOutcome > 0:
		out.Disposition = gantryv1.Disposition_DISPOSITION_FAIL
		out.Reason = fmt.Sprintf("%d/%d required outcome checks passed", outcomePassing, requiredOutcome)
	default:
		// Preconditions satisfied but no required outcome checks: nothing asserts
		// success, so it is not counted as a pass.
		out.Disposition = gantryv1.Disposition_DISPOSITION_UNSPECIFIED
		out.Reason = "no required outcome checks"
	}
	return out
}

// evalCheck reports whether a check passes. BOOL checks use pass directly;
// NUMERIC checks with an op recompute (value op threshold) so the disposition
// never trusts a stale pass flag; a NUMERIC check with no op falls back to pass.
func evalCheck(c *gantryv1.Check) bool {
	if c.Kind == gantryv1.CheckKind_CHECK_KIND_NUMERIC && c.Op != "" {
		return compare(c.Value, c.Op, c.Threshold)
	}
	return c.Pass
}

// compare evaluates (a op b) for the numeric comparison ops. An unknown op is
// treated as failing (conservative: an unrecognised assertion is not a pass).
func compare(a float64, op string, b float64) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}
