package mcp

import (
	"context"
	"testing"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

type fakeEvals struct {
	trials    []*gantryv1.Trial
	submitted *gantryv1.Verdict
}

func (f *fakeEvals) Trials(_ context.Context, _ string) ([]*gantryv1.Trial, error) {
	return f.trials, nil
}

func (f *fakeEvals) SubmitVerdict(_ context.Context, trialID string, v *gantryv1.Verdict) (*gantryv1.Trial, error) {
	f.submitted = v
	return &gantryv1.Trial{Id: trialID, Outcome: &gantryv1.TrialOutcome{Disposition: gantryv1.Disposition_DISPOSITION_PASS}}, nil
}

func TestListTrialsTool(t *testing.T) {
	fe := &fakeEvals{trials: []*gantryv1.Trial{
		{Id: "t1", ScenarioId: "s1", Attempt: 0, VideoChunkIds: []string{"c1"}, Outcome: &gantryv1.TrialOutcome{Disposition: gantryv1.Disposition_DISPOSITION_FAIL}},
	}}
	d := Deps{Evals: fe}
	_, res, err := d.listTrials(context.Background(), nil, listTrialsArgs{RunID: "r1"})
	if err != nil {
		t.Fatalf("listTrials: %v", err)
	}
	if len(res.Trials) != 1 || res.Trials[0].ID != "t1" || res.Trials[0].Disposition != "FAIL" {
		t.Fatalf("unexpected trials: %+v", res.Trials)
	}
	if res.Trials[0].VideoChunks[0] != "c1" {
		t.Fatalf("video chunk not surfaced: %+v", res.Trials[0])
	}
	if _, _, err := d.listTrials(context.Background(), nil, listTrialsArgs{}); err == nil {
		t.Fatal("empty run_id should error")
	}
}

func TestSubmitVerdictTool(t *testing.T) {
	fe := &fakeEvals{}
	d := Deps{Evals: fe}
	// A boolean outcome check.
	_, res, err := d.submitVerdict(context.Background(), nil, submitVerdictArgs{
		TrialID: "t1", VerifierID: "agent-claude", VerifierVersion: "1",
		Checks: []verdictCheckArg{{Name: "placed", Phase: "outcome", Required: true, Pass: true}},
	})
	if err != nil {
		t.Fatalf("submitVerdict: %v", err)
	}
	if res.Disposition != "PASS" {
		t.Fatalf("want PASS, got %q", res.Disposition)
	}
	if fe.submitted.Checks[0].Phase != gantryv1.Phase_PHASE_OUTCOME || fe.submitted.Checks[0].Kind != gantryv1.CheckKind_CHECK_KIND_BOOL {
		t.Fatalf("bool check mapped wrong: %+v", fe.submitted.Checks[0])
	}
	// An op makes it a numeric check.
	_, _, err = d.submitVerdict(context.Background(), nil, submitVerdictArgs{
		TrialID: "t1", VerifierID: "agent-claude", VerifierVersion: "1",
		Checks: []verdictCheckArg{{Name: "task_time_s", Phase: "outcome", Op: "<=", Threshold: 30, Value: 20}},
	})
	if err != nil {
		t.Fatalf("submitVerdict numeric: %v", err)
	}
	if fe.submitted.Checks[0].Kind != gantryv1.CheckKind_CHECK_KIND_NUMERIC {
		t.Fatalf("op should imply NUMERIC: %+v", fe.submitted.Checks[0])
	}
	if _, _, err := d.submitVerdict(context.Background(), nil, submitVerdictArgs{TrialID: "t1"}); err == nil {
		t.Fatal("missing verifier_id should error")
	}
}
