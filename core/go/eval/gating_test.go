package eval

import (
	"context"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestGateBootstrapRequiresMinScored(t *testing.T) {
	// Regression for finding #2: a thin/empty first run must NOT bootstrap-pass
	// or become promotable — that would seed a degenerate champion.
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, _ := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:      "s",
		Scenarios: []*gantryv1.Scenario{{Id: "s1", TrialBudget: 20, MinScored: 20}},
	})
	run := scoreRun(t, svc, suite.Id, "sha:thin", 3, 3) // 3 scored, no baseline
	res, err := svc.EvaluateGate(ctx, run.Id)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if res.Passed || !res.Inconclusive {
		t.Fatalf("thin bootstrap must be inconclusive, not pass: %+v", res)
	}
	if _, err := svc.PromoteBaseline(ctx, run.Id, ""); err == nil {
		t.Fatal("an inconclusive run must not be promotable")
	}
}

func TestGateRejectsNonInferiorOnNonRateMetric(t *testing.T) {
	// Regression for finding #3: non_inferior on a non-rate metric must not
	// silently compare success_rate's Wilson bound against a zero baseline.
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, _ := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:        "s",
		GateJson:    `[{"metric":"task_time_s","op":"non_inferior","margin":0.5}]`,
		MetricsJson: `[{"name":"task_time_s","check":"task_time_s","agg":"p50"}]`,
		Scenarios:   []*gantryv1.Scenario{{Id: "s1", TrialBudget: 1, MinScored: 1}},
	})
	run, _ := svc.StartRun(ctx, suite.Id, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	tr, _ := svc.OpenTrial(ctx, run.Id, "s1", 0, "", 0)
	if _, err := svc.SubmitVerdict(ctx, tr.Id, &gantryv1.Verdict{VerifierId: "t", VerifierVersion: "1", Checks: []*gantryv1.Check{
		{Name: "task_time_s", Phase: gantryv1.Phase_PHASE_OUTCOME, Required: true, Kind: gantryv1.CheckKind_CHECK_KIND_NUMERIC, Op: "<=", Threshold: 30, Value: 20},
	}}); err != nil {
		t.Fatalf("SubmitVerdict: %v", err)
	}
	if _, err := svc.CloseTrial(ctx, tr.Id, 0, nil); err != nil {
		t.Fatalf("CloseTrial: %v", err)
	}
	res, err := svc.EvaluateGate(ctx, run.Id)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if res.Passed {
		t.Fatalf("non_inferior on a non-rate metric must not pass: %s", res.Checks[0].Detail)
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

func TestGatePerScenarioMinScored(t *testing.T) {
	// Regression for finding #9b: adequacy is PER SCENARIO. Over-scoring one
	// scenario must not paper over a starved one — even when the run-wide scored
	// count meets the summed minimum, the run is inconclusive.
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, err := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name: "two-scenario",
		Scenarios: []*gantryv1.Scenario{
			{Id: "easy", Name: "easy", TrialBudget: 10, MinScored: 5},
			{Id: "hard", Name: "hard", TrialBudget: 10, MinScored: 5},
		},
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}
	run, err := svc.StartRun(ctx, suite.Id, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	open := func(scenario string, n int) {
		for i := 0; i < n; i++ {
			tr, err := svc.OpenTrial(ctx, run.Id, scenario, uint32(i), "", uint64(i))
			if err != nil {
				t.Fatalf("OpenTrial: %v", err)
			}
			if _, err := svc.SubmitVerdict(ctx, tr.Id, &gantryv1.Verdict{
				VerifierId: "t", VerifierVersion: "1",
				Checks: []*gantryv1.Check{check("ok", gantryv1.Phase_PHASE_OUTCOME, true, true)},
			}); err != nil {
				t.Fatalf("SubmitVerdict: %v", err)
			}
			if _, err := svc.CloseTrial(ctx, tr.Id, 0, nil); err != nil {
				t.Fatalf("CloseTrial: %v", err)
			}
		}
	}
	// 9 on "easy" + 1 on "hard" = 10 scored run-wide (meets the summed min of 10,
	// which the old run-wide check would wave through) — but "hard" has only 1 of
	// its required 5, so the run must be inconclusive.
	open("easy", 9)
	open("hard", 1)
	res, err := svc.EvaluateGate(ctx, run.Id)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if res.Passed || !res.Inconclusive {
		t.Fatalf("a starved scenario must make the run inconclusive: %+v", res)
	}
}

func TestVoidRateComputedAndGateable(t *testing.T) {
	// Regression for finding #9a: void_rate is an emitted, gateable metric. A
	// flaky bench (many VOIDs) fails a void_rate ceiling even though the
	// success_rate among the trials that DID score is a perfect 1.0.
	ctx := context.Background()
	svc := newGatingSvc(t)
	suite, err := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:      "void-guard",
		GateJson:  `[{"metric":"void_rate","op":"<=","margin":0.25}]`,
		Scenarios: []*gantryv1.Scenario{{Id: "s1", TrialBudget: 10, MinScored: 1}},
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}
	run, err := svc.StartRun(ctx, suite.Id, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	mk := func(i int, void bool) {
		tr, err := svc.OpenTrial(ctx, run.Id, "s1", uint32(i), "", uint64(i))
		if err != nil {
			t.Fatalf("OpenTrial: %v", err)
		}
		checks := []*gantryv1.Check{check("ok", gantryv1.Phase_PHASE_OUTCOME, true, true)}
		if void {
			// A failed required precondition VOIDs the trial (staging mistake).
			checks = []*gantryv1.Check{check("staged", gantryv1.Phase_PHASE_PRECONDITION, true, false)}
		}
		if _, err := svc.SubmitVerdict(ctx, tr.Id, &gantryv1.Verdict{
			VerifierId: "t", VerifierVersion: "1", Checks: checks,
		}); err != nil {
			t.Fatalf("SubmitVerdict: %v", err)
		}
		if _, err := svc.CloseTrial(ctx, tr.Id, 0, nil); err != nil {
			t.Fatalf("CloseTrial: %v", err)
		}
	}
	// 3 PASS + 3 VOID → void_rate = 3/6 = 0.5 (> 0.25 ceiling); success_rate = 1.0.
	for i := 0; i < 3; i++ {
		mk(i, false)
	}
	for i := 3; i < 6; i++ {
		mk(i, true)
	}
	res, err := svc.EvaluateGate(ctx, run.Id)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if len(res.Checks) != 1 {
		t.Fatalf("want one gate check, got %d", len(res.Checks))
	}
	if math.Abs(res.Checks[0].CandidateValue-0.5) > 1e-9 {
		t.Fatalf("void_rate not computed/surfaced: candidate = %.4f, want 0.5 (%s)", res.Checks[0].CandidateValue, res.Checks[0].Detail)
	}
	if res.Passed {
		t.Fatalf("void_rate 0.5 must fail a 0.25 ceiling: %s", res.Checks[0].Detail)
	}
}

func TestPromoteBaselineConcurrentCAS(t *testing.T) {
	// Regression for finding #7: two concurrently-passing runs must not let the
	// older promotion clobber the newer one. Promotion is a DB compare-and-swap on
	// promoted_ns, so the run with the later promote is the deterministic champion
	// no matter how the writes interleave (last-write-wins would be flaky here).
	ctx := context.Background()
	db, err := benchdb.Open(ctx, filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exp := experiments.NewService(db)
	svc := NewService(db, exp)

	suite, err := svc.UpsertSuite(ctx, &gantryv1.Suite{
		Name:      "cas",
		Scenarios: []*gantryv1.Scenario{{Id: "s1", TrialBudget: 20, MinScored: 20}},
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}
	// Both runs bootstrap-pass (we only gate here; no champion is promoted yet).
	hiRun := scoreRun(t, svc, suite.Id, "sha:hi", 20, 20) // rate 1.0
	if _, err := svc.EvaluateGate(ctx, hiRun.Id); err != nil {
		t.Fatalf("gate hi: %v", err)
	}
	loRun := scoreRun(t, svc, suite.Id, "sha:lo", 18, 20) // rate 0.9
	if _, err := svc.EvaluateGate(ctx, loRun.Id); err != nil {
		t.Fatalf("gate lo: %v", err)
	}

	// hi's promote carries a strictly-newer promoted_ns, so hi must win the CAS.
	hi := NewService(db, exp, WithClock(func() time.Time { return time.Unix(0, 2000) }))
	lo := NewService(db, exp, WithClock(func() time.Time { return time.Unix(0, 1000) }))

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		promoter, runID := hi, hiRun.Id
		if i%2 == 1 {
			promoter, runID = lo, loRun.Id
		}
		wg.Add(1)
		go func(p *Service, id string) {
			defer wg.Done()
			if _, err := p.PromoteBaseline(ctx, id, ""); err != nil {
				t.Errorf("PromoteBaseline: %v", err)
			}
		}(promoter, runID)
	}
	wg.Wait()

	champ, err := svc.GetBaseline(ctx, suite.Id, "")
	if err != nil {
		t.Fatalf("GetBaseline: %v", err)
	}
	if champ.FromRunId != hiRun.Id {
		t.Fatalf("the newer promote must win the CAS: champion from %s, want hi run %s", champ.FromRunId, hiRun.Id)
	}
	if math.Abs(champ.SuccessRate-1.0) > 1e-9 {
		t.Fatalf("champion rate = %.4f, want 1.0 (the hi run)", champ.SuccessRate)
	}
}
