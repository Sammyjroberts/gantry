package eval_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/eval"
	"github.com/Sammyjroberts/gantry/core/go/experiments"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// newSvc builds an eval.Service over a fresh migrated bench DB, wired to a real
// experiments.Service as the trial bracketer (so trials really do open/stop
// experiments). It returns the experiments service too, for cross-checking.
func newSvc(t *testing.T) (*eval.Service, *experiments.Service) {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	expSvc := experiments.NewService(db)
	return eval.NewService(db, expSvc), expSvc
}

func mustSuite(t *testing.T, svc *eval.Service) *gantryv1.Suite {
	t.Helper()
	su, err := svc.UpsertSuite(context.Background(), &gantryv1.Suite{
		Name:        "arm-pickplace",
		SubjectKind: "policy",
		Scenarios: []*gantryv1.Scenario{
			{Id: "pick-a", Name: "pick red block", TrialBudget: 50, MinScored: 20},
		},
	})
	if err != nil {
		t.Fatalf("UpsertSuite: %v", err)
	}
	return su
}

func TestSuiteLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	su := mustSuite(t, svc)
	if su.Id == "" {
		t.Fatal("UpsertSuite returned empty id")
	}
	if su.CreatedNs == 0 || su.UpdatedNs == 0 {
		t.Fatalf("timestamps unset: %+v", su)
	}
	if len(su.Scenarios) != 1 || su.Scenarios[0].Id != "pick-a" || su.Scenarios[0].TrialBudget != 50 {
		t.Fatalf("scenarios round-trip mismatch: %+v", su.Scenarios)
	}

	// Update in place: created_ns preserved, updated_ns advances, scenarios replaced.
	su.Name = "arm-pickplace-v2"
	su.Scenarios = []*gantryv1.Scenario{
		{Id: "pick-a", Name: "pick red block", TrialBudget: 30},
		{Id: "pick-b", Name: "pick blue block", TrialBudget: 30},
	}
	up, err := svc.UpsertSuite(ctx, su)
	if err != nil {
		t.Fatalf("UpsertSuite update: %v", err)
	}
	if up.CreatedNs != su.CreatedNs {
		t.Fatalf("created_ns changed on update: %d -> %d", su.CreatedNs, up.CreatedNs)
	}
	if up.UpdatedNs < su.UpdatedNs {
		t.Fatalf("updated_ns went backwards: %d -> %d", su.UpdatedNs, up.UpdatedNs)
	}
	if len(up.Scenarios) != 2 {
		t.Fatalf("scenarios not replaced: %+v", up.Scenarios)
	}

	got, err := svc.GetSuite(ctx, su.Id)
	if err != nil || got.Name != "arm-pickplace-v2" {
		t.Fatalf("GetSuite: %v / %+v", err, got)
	}
	list, err := svc.ListSuites(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSuites: %v / len=%d", err, len(list))
	}
}

func TestUpsertSuiteRejectsEmptyName(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.UpsertSuite(context.Background(), &gantryv1.Suite{}); !errors.Is(err, eval.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty name, got %v", err)
	}
}

func TestRegisterSubjectIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	sub := &gantryv1.Subject{Kind: "policy", Uri: "models://act", Version: "rc1", Digest: "sha256:abc"}
	if _, err := svc.RegisterSubject(ctx, sub); err != nil {
		t.Fatalf("RegisterSubject: %v", err)
	}
	// Same digest again is a no-op, not a conflict.
	if _, err := svc.RegisterSubject(ctx, sub); err != nil {
		t.Fatalf("RegisterSubject (repeat): %v", err)
	}
	if _, err := svc.RegisterSubject(ctx, &gantryv1.Subject{Kind: "policy"}); !errors.Is(err, eval.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty digest, got %v", err)
	}
}

func TestStartRunIdempotency(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	su := mustSuite(t, svc)
	cand := &gantryv1.Subject{Kind: "policy", Digest: "sha256:cand"}

	run1, err := svc.StartRun(ctx, su.Id, cand, "latest", "arm=so101", 1, "ci-run-42")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run1.Status != gantryv1.RunStatus_RUN_STATUS_PENDING {
		t.Fatalf("new run status = %v, want PENDING", run1.Status)
	}
	// Retry with the same key re-attaches to the same run.
	run2, err := svc.StartRun(ctx, su.Id, cand, "latest", "arm=so101", 1, "ci-run-42")
	if err != nil {
		t.Fatalf("StartRun (retry): %v", err)
	}
	if run2.Id != run1.Id {
		t.Fatalf("idempotent StartRun made a new run: %s != %s", run2.Id, run1.Id)
	}

	if _, err := svc.StartRun(ctx, "nope", cand, "", "", 1, ""); !errors.Is(err, eval.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown suite, got %v", err)
	}
}

func TestTrialLifecycleIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, expSvc := newSvc(t)
	su := mustSuite(t, svc)
	run, err := svc.StartRun(ctx, su.Id, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Open a trial: it brackets an experiment.
	tr, err := svc.OpenTrial(ctx, run.Id, "pick-a", 0, "cell-07", 99)
	if err != nil {
		t.Fatalf("OpenTrial: %v", err)
	}
	if tr.ExperimentId == "" || tr.StartedNs == 0 {
		t.Fatalf("trial not bracketed: %+v", tr)
	}
	if _, err := expSvc.Get(ctx, tr.ExperimentId); err != nil {
		t.Fatalf("trial experiment missing: %v", err)
	}

	// Replay is idempotent: same trial + same experiment, no second experiment.
	again, err := svc.OpenTrial(ctx, run.Id, "pick-a", 0, "cell-07", 99)
	if err != nil {
		t.Fatalf("OpenTrial (replay): %v", err)
	}
	if again.Id != tr.Id || again.ExperimentId != tr.ExperimentId {
		t.Fatalf("OpenTrial not idempotent: %+v vs %+v", again, tr)
	}

	// Close ends the experiment and stamps the trial.
	closed, err := svc.CloseTrial(ctx, tr.Id, 0, []string{"chunk-1"})
	if err != nil {
		t.Fatalf("CloseTrial: %v", err)
	}
	if closed.EndedNs == 0 {
		t.Fatal("CloseTrial left ended_ns = 0")
	}
	if exp, _ := expSvc.Get(ctx, tr.ExperimentId); exp.EndNs == 0 {
		t.Fatal("underlying experiment still running after CloseTrial")
	}

	// Closing again is a no-op (idempotent) and preserves ended_ns.
	reclosed, err := svc.CloseTrial(ctx, tr.Id, 0, nil)
	if err != nil {
		t.Fatalf("CloseTrial (repeat): %v", err)
	}
	if reclosed.EndedNs != closed.EndedNs {
		t.Fatalf("ended_ns changed on re-close: %d -> %d", closed.EndedNs, reclosed.EndedNs)
	}
	if len(reclosed.VideoChunkIds) != 1 || reclosed.VideoChunkIds[0] != "chunk-1" {
		t.Fatalf("video chunks not preserved: %+v", reclosed.VideoChunkIds)
	}

	if _, err := svc.OpenTrial(ctx, "ghost", "pick-a", 0, "", 0); !errors.Is(err, eval.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown run, got %v", err)
	}
}

func TestSubmitVerdictUpsert(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	su := mustSuite(t, svc)
	run, _ := svc.StartRun(ctx, su.Id, &gantryv1.Subject{Digest: "d"}, "", "", 1, "")
	tr, _ := svc.OpenTrial(ctx, run.Id, "pick-a", 0, "cell-07", 1)

	v := &gantryv1.Verdict{
		VerifierId:      "topdown-yolo",
		VerifierVersion: "1.0.0",
		VerifierDigest:  "sha256:w1",
		Checks: []*gantryv1.Check{
			{Name: "placed", Phase: gantryv1.Phase_PHASE_OUTCOME, Required: true, Kind: gantryv1.CheckKind_CHECK_KIND_BOOL, Pass: true},
		},
	}
	got, err := svc.SubmitVerdict(ctx, tr.Id, v)
	if err != nil {
		t.Fatalf("SubmitVerdict: %v", err)
	}
	if len(got.Verdicts) != 1 || got.Verdicts[0].ScoredNs == 0 {
		t.Fatalf("verdict not stored/stamped: %+v", got.Verdicts)
	}

	// Same verifier+version replaces (still one verdict).
	v.Checks[0].Pass = false
	got, err = svc.SubmitVerdict(ctx, tr.Id, v)
	if err != nil {
		t.Fatalf("SubmitVerdict (replace): %v", err)
	}
	if len(got.Verdicts) != 1 || got.Verdicts[0].Checks[0].Pass {
		t.Fatalf("verdict not replaced in place: %+v", got.Verdicts)
	}

	// A new version adds a distinct verdict (re-grade).
	v2 := &gantryv1.Verdict{VerifierId: "topdown-yolo", VerifierVersion: "1.1.0"}
	got, err = svc.SubmitVerdict(ctx, tr.Id, v2)
	if err != nil {
		t.Fatalf("SubmitVerdict (new version): %v", err)
	}
	if len(got.Verdicts) != 2 {
		t.Fatalf("re-grade did not add a verdict: %+v", got.Verdicts)
	}

	// GetRun surfaces the trial with its verdicts.
	_, trials, err := svc.GetRun(ctx, run.Id)
	if err != nil || len(trials) != 1 || len(trials[0].Verdicts) != 2 {
		t.Fatalf("GetRun trials/verdicts: %v / %+v", err, trials)
	}

	if _, err := svc.SubmitVerdict(ctx, "ghost", v); !errors.Is(err, eval.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown trial, got %v", err)
	}
}
