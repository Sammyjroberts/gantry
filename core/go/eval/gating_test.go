package eval

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/experiments"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ---- unit: combine (checks -> disposition) ----

func check(name string, phase gantryv1.Phase, required, pass bool) *gantryv1.Check {
	return &gantryv1.Check{Name: name, Phase: phase, Required: required, Kind: gantryv1.CheckKind_CHECK_KIND_BOOL, Pass: pass}
}

func TestDeriveOutcome(t *testing.T) {
	pre := gantryv1.Phase_PHASE_PRECONDITION
	dur := gantryv1.Phase_PHASE_DURING
	out := gantryv1.Phase_PHASE_OUTCOME

	cases := []struct {
		name   string
		checks []*gantryv1.Check
		want   gantryv1.Disposition
	}{
		{"precondition fail voids", []*gantryv1.Check{check("space_ready", pre, true, false), check("placed", out, true, true)}, gantryv1.Disposition_DISPOSITION_VOID},
		{"interlock fail fails", []*gantryv1.Check{check("space_ready", pre, true, true), check("estop", dur, true, false)}, gantryv1.Disposition_DISPOSITION_FAIL},
		{"all outcome pass -> pass", []*gantryv1.Check{check("space_ready", pre, true, true), check("placed", out, true, true)}, gantryv1.Disposition_DISPOSITION_PASS},
		{"one outcome fail -> fail", []*gantryv1.Check{check("a", out, true, true), check("b", out, true, false)}, gantryv1.Disposition_DISPOSITION_FAIL},
		{"no checks -> unspecified", nil, gantryv1.Disposition_DISPOSITION_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveOutcome([]*gantryv1.Verdict{{Checks: tc.checks}})
			if got.Disposition != tc.want {
				t.Fatalf("disposition = %v, want %v (%s)", got.Disposition, tc.want, got.Reason)
			}
		})
	}
}

func TestDeriveOutcomeNumericAndMetrics(t *testing.T) {
	out := gantryv1.Phase_PHASE_OUTCOME
	// task_time_s <= 30 passes at 27.3 and surfaces as a metric.
	v := &gantryv1.Verdict{Checks: []*gantryv1.Check{
		{Name: "task_time_s", Phase: out, Required: true, Kind: gantryv1.CheckKind_CHECK_KIND_NUMERIC, Op: "<=", Threshold: 30, Value: 27.3},
	}}
	got := deriveOutcome([]*gantryv1.Verdict{v})
	if got.Disposition != gantryv1.Disposition_DISPOSITION_PASS {
		t.Fatalf("numeric pass expected, got %v (%s)", got.Disposition, got.Reason)
	}
	if got.Metrics["task_time_s"] != 27.3 {
		t.Fatalf("metric not surfaced: %+v", got.Metrics)
	}
	// A slower run fails the same threshold.
	v.Checks[0].Value = 41
	if deriveOutcome([]*gantryv1.Verdict{v}).Disposition != gantryv1.Disposition_DISPOSITION_FAIL {
		t.Fatal("numeric over-threshold should fail")
	}
}

// ---- unit: Wilson lower bound ----

func TestWilsonLower(t *testing.T) {
	z := zFor(0.95)
	approx := func(got, want, tol float64) bool { return math.Abs(got-want) <= tol }
	if l := wilsonLower(46, 50, z); !approx(l, 0.8118, 0.002) {
		t.Fatalf("wilsonLower(46,50) = %.4f, want ~0.8118", l)
	}
	if l := wilsonLower(20, 20, z); !approx(l, 0.8389, 0.002) {
		t.Fatalf("wilsonLower(20,20) = %.4f, want ~0.8389", l)
	}
	if l := wilsonLower(0, 0, z); l != 0 {
		t.Fatalf("wilsonLower(0,0) = %.4f, want 0", l)
	}
	// The point of the gate: even a perfect 20/20 can't prove non-inferiority to
	// 0.95 within 3 points, because its 95% lower bound (~0.84) is below 0.92.
	if wilsonLower(20, 20, z) >= 0.92 {
		t.Fatal("20/20 should NOT clear a 0.92 non-inferiority threshold")
	}
}

// ---- unit: aggregate excludes VOID ----

func TestAggregateExcludesVoid(t *testing.T) {
	mk := func(d gantryv1.Disposition) *gantryv1.Trial {
		return &gantryv1.Trial{Outcome: &gantryv1.TrialOutcome{Disposition: d}}
	}
	trials := []*gantryv1.Trial{
		mk(gantryv1.Disposition_DISPOSITION_PASS),
		mk(gantryv1.Disposition_DISPOSITION_PASS),
		mk(gantryv1.Disposition_DISPOSITION_FAIL),
		mk(gantryv1.Disposition_DISPOSITION_VOID), // must not move the rate
	}
	m, err := aggregate(trials, "")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if m.Pass != 2 || m.Fail != 1 || m.Void != 1 {
		t.Fatalf("counts = %+v", m)
	}
	if got := m.Values["success_rate"]; math.Abs(got-2.0/3.0) > 1e-9 {
		t.Fatalf("success_rate = %.4f, want 0.6667 (VOID excluded)", got)
	}
}

// ---- integration: bootstrap -> promote -> gate ----

func newGatingSvc(t *testing.T) *Service {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewService(db, experiments.NewService(db))
}

// scoreRun opens `total` trials for a fresh run and passes the first `passCount`.
func scoreRun(t *testing.T, svc *Service, suiteID, digest string, passCount, total int) *gantryv1.EvalRun {
	t.Helper()
	ctx := context.Background()
	run, err := svc.StartRun(ctx, suiteID, &gantryv1.Subject{Digest: digest}, "latest", "", 1, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for i := 0; i < total; i++ {
		tr, err := svc.OpenTrial(ctx, run.Id, "s1", uint32(i), "", uint64(i))
		if err != nil {
			t.Fatalf("OpenTrial: %v", err)
		}
		if _, err := svc.SubmitVerdict(ctx, tr.Id, &gantryv1.Verdict{
			VerifierId: "telemetry", VerifierVersion: "1",
			Checks: []*gantryv1.Check{check("ok", gantryv1.Phase_PHASE_OUTCOME, true, i < passCount)},
		}); err != nil {
			t.Fatalf("SubmitVerdict: %v", err)
		}
		if _, err := svc.CloseTrial(ctx, tr.Id, 0, nil); err != nil {
			t.Fatalf("CloseTrial: %v", err)
		}
	}
	return run
}

func TestGateBootstrapPromoteAndCompare(t *testing.T) {
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, err := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:      "arm-pickplace",
		Scenarios: []*gantryv1.Scenario{{Id: "s1", Name: "pick", TrialBudget: 20, MinScored: 20}},
		// default gate: success_rate non_inferior, margin 0.03
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}

	// Bootstrap: no baseline yet -> the non-inferiority check passes so the first
	// candidate can seed. 16/20 = 0.80.
	boot := scoreRun(t, svc, suite.Id, "sha:boot", 16, 20)
	res, err := svc.EvaluateGate(ctx, boot.Id)
	if err != nil {
		t.Fatalf("EvaluateGate(boot): %v", err)
	}
	if !res.Passed || res.Inconclusive {
		t.Fatalf("bootstrap gate should pass: %+v", res)
	}
	if res.Pass != 16 || res.Fail != 4 {
		t.Fatalf("bootstrap tallies = pass %d fail %d", res.Pass, res.Fail)
	}
	base, err := svc.PromoteBaseline(ctx, boot.Id, "")
	if err != nil {
		t.Fatalf("PromoteBaseline: %v", err)
	}
	if math.Abs(base.SuccessRate-0.8) > 1e-9 {
		t.Fatalf("baseline rate = %.4f, want 0.80", base.SuccessRate)
	}
	// Promotion is idempotent.
	if again, err := svc.PromoteBaseline(ctx, boot.Id, ""); err != nil || again.FromRunId != boot.Id {
		t.Fatalf("idempotent promote: %v / %+v", err, again)
	}

	// A clearly-better candidate (20/20 = 1.0) clears non-inferiority to 0.80
	// (threshold 0.77; Wilson lower ~0.84).
	good := scoreRun(t, svc, suite.Id, "sha:good", 20, 20)
	res, err = svc.EvaluateGate(ctx, good.Id)
	if err != nil {
		t.Fatalf("EvaluateGate(good): %v", err)
	}
	if !res.Passed {
		t.Fatalf("20/20 vs 0.80 baseline should pass: %s", res.Checks[0].Detail)
	}

	// A regressed candidate (14/20 = 0.70) fails against the 0.80 baseline.
	bad := scoreRun(t, svc, suite.Id, "sha:bad", 14, 20)
	res, err = svc.EvaluateGate(ctx, bad.Id)
	if err != nil {
		t.Fatalf("EvaluateGate(bad): %v", err)
	}
	if res.Passed {
		t.Fatalf("14/20 vs 0.80 baseline should fail: %s", res.Checks[0].Detail)
	}
	// A failed gate cannot be promoted.
	if _, err := svc.PromoteBaseline(ctx, bad.Id, ""); err == nil {
		t.Fatal("promoting a failed run should error")
	}

	// Promote the good run: the champion advances to 1.0.
	if _, err := svc.PromoteBaseline(ctx, good.Id, ""); err != nil {
		t.Fatalf("PromoteBaseline(good): %v", err)
	}
	champ, err := svc.GetBaseline(ctx, suite.Id, "")
	if err != nil || champ.FromRunId != good.Id || math.Abs(champ.SuccessRate-1.0) > 1e-9 {
		t.Fatalf("champion not advanced: %v / %+v", err, champ)
	}
}

func TestGateInconclusiveBelowMinScored(t *testing.T) {
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, _ := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:      "s",
		Scenarios: []*gantryv1.Scenario{{Id: "s1", TrialBudget: 20, MinScored: 20}},
	})
	// Establish a champion first, so the candidate is compared (not bootstrapped).
	boot := scoreRun(t, svc, suite.Id, "sha:boot", 18, 20)
	if _, err := svc.EvaluateGate(ctx, boot.Id); err != nil {
		t.Fatalf("EvaluateGate(boot): %v", err)
	}
	if _, err := svc.PromoteBaseline(ctx, boot.Id, ""); err != nil {
		t.Fatalf("PromoteBaseline: %v", err)
	}
	// Only 5 trials scored — below the min_scored=20 power threshold.
	run := scoreRun(t, svc, suite.Id, "sha:thin", 5, 5)
	res, err := svc.EvaluateGate(ctx, run.Id)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if !res.Inconclusive || res.Passed {
		t.Fatalf("want inconclusive+not-passed, got %+v", res)
	}
}
