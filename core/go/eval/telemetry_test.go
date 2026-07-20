package eval

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/experiments"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// fakeSampler returns canned aggregates keyed by channel, plus a presence flag.
type fakeSampler struct {
	vals    map[string]float64
	present bool
}

func (f fakeSampler) Aggregate(_ context.Context, _, channel, _, _ string, _, _ uint64) (float64, bool, error) {
	return f.vals[channel], f.present, nil
}

func newTelemetrySvc(t *testing.T, s Sampler) *Service {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewService(db, experiments.NewService(db), WithSampler(s))
}

// telemetrySuite declares a telemetry verifier: precondition staged==4 and
// outcome gripper_contact>=1.
const telemetryVerifierJSON = `{"telemetry":{"verifier_id":"telemetry","version":"1","checks":[
  {"name":"space_ready","phase":"precondition","required":true,"channel":"staged","col":"v_i64","agg":"max","op":"==","threshold":4},
  {"name":"placed","phase":"outcome","required":true,"channel":"gripper_contact","col":"v_bool","agg":"max","op":">=","threshold":1}
]}}`

func telemetrySuite(t *testing.T, svc *Service) string {
	t.Helper()
	su, err := svc.UpsertSuite(context.Background(), &gantryv1.Suite{
		Name:               "arm-pickplace",
		VerifierConfigJson: telemetryVerifierJSON,
		Scenarios:          []*gantryv1.Scenario{{Id: "s1", TrialBudget: 1, MinScored: 1}},
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}
	return su.Id
}

func runOneTrial(t *testing.T, svc *Service, suiteID string) *gantryv1.Trial {
	t.Helper()
	ctx := context.Background()
	run, err := svc.StartRun(ctx, suiteID, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	tr, err := svc.OpenTrial(ctx, run.Id, "s1", 0, "arm-1", 0)
	if err != nil {
		t.Fatalf("OpenTrial: %v", err)
	}
	// CloseTrial auto-scores via the wired Sampler.
	closed, err := svc.CloseTrial(ctx, tr.Id, 0, nil)
	if err != nil {
		t.Fatalf("CloseTrial: %v", err)
	}
	return closed
}

func TestTelemetryAutoScorePass(t *testing.T) {
	svc := newTelemetrySvc(t, fakeSampler{vals: map[string]float64{"staged": 4, "gripper_contact": 1}, present: true})
	suiteID := telemetrySuite(t, svc)
	closed := runOneTrial(t, svc, suiteID)

	if len(closed.Verdicts) != 1 || len(closed.Verdicts[0].Checks) != 2 {
		t.Fatalf("expected 1 verdict with 2 checks, got %+v", closed.Verdicts)
	}
	if closed.Outcome == nil || closed.Outcome.Disposition != gantryv1.Disposition_DISPOSITION_PASS {
		t.Fatalf("want PASS, got %+v", closed.Outcome)
	}
	if closed.Verdicts[0].ScoredFrom == nil || closed.Verdicts[0].ScoredFrom.RangeEndNs == 0 {
		t.Fatalf("scored_from window not recorded: %+v", closed.Verdicts[0].ScoredFrom)
	}
}

func TestTelemetryAutoScoreOutcomeFail(t *testing.T) {
	// Staged correctly (precondition ok) but no grasp -> FAIL, not VOID.
	svc := newTelemetrySvc(t, fakeSampler{vals: map[string]float64{"staged": 4, "gripper_contact": 0}, present: true})
	closed := runOneTrial(t, svc, telemetrySuite(t, svc))
	if closed.Outcome.Disposition != gantryv1.Disposition_DISPOSITION_FAIL {
		t.Fatalf("want FAIL, got %+v", closed.Outcome)
	}
}

func TestTelemetryAutoScorePreconditionVoids(t *testing.T) {
	// Only 3 blocks staged -> precondition fails -> VOID (excluded from the rate).
	svc := newTelemetrySvc(t, fakeSampler{vals: map[string]float64{"staged": 3, "gripper_contact": 1}, present: true})
	closed := runOneTrial(t, svc, telemetrySuite(t, svc))
	if closed.Outcome.Disposition != gantryv1.Disposition_DISPOSITION_VOID {
		t.Fatalf("want VOID, got %+v", closed.Outcome)
	}
}

func TestTelemetryMissingSamplesFail(t *testing.T) {
	// No telemetry in the window -> checks fail (no evidence of success).
	svc := newTelemetrySvc(t, fakeSampler{present: false})
	closed := runOneTrial(t, svc, telemetrySuite(t, svc))
	if closed.Outcome.Disposition == gantryv1.Disposition_DISPOSITION_PASS {
		t.Fatalf("missing telemetry must not pass: %+v", closed.Outcome)
	}
}

func TestNoSamplerLeavesTrialUnscored(t *testing.T) {
	// Without a Sampler, CloseTrial does not auto-score (M0 behaviour intact).
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, experiments.NewService(db))
	closed := runOneTrial(t, svc, telemetrySuite(t, svc))
	if len(closed.Verdicts) != 0 {
		t.Fatalf("no sampler should mean no auto-verdict, got %+v", closed.Verdicts)
	}
}
