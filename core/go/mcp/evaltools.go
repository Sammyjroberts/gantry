package mcp

import (
	"context"
	"fmt"

	"github.com/Sammyjroberts/gantry/core/go/auth"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerEvalTools wires the verifier tools (list_trials, submit_verdict) onto
// s. Called only when Deps.Evals is non-nil. Together with the read tools
// (get_window/query_sql) and the linked video, they let an MCP client act as an
// eval verifier: inspect a run's trials, reason over the evidence, and write a
// pass/fail verdict per trial.
func registerEvalTools(s *mcpsdk.Server, d Deps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_trials",
		Description: "List a run's trials: id, scenario, attempt, station, linked video chunk ids, telemetry window (started/ended ns), and current disposition. Read-only. Use this to find trials to score, then submit_verdict for each.",
	}, d.listTrials)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "submit_verdict",
		Description: "Submit a verifier verdict for a trial: a set of checks across phases (precondition/during/outcome), each pass/fail with an optional numeric value. Recomputes the trial disposition (a failed required precondition VOIDs the trial; a failed required outcome check fails it). Upserts on (verifier_id, verifier_version) so re-scoring replaces in place.",
	}, d.submitVerdict)
}

// ---- list_trials ----

type listTrialsArgs struct {
	RunID string `json:"run_id" jsonschema:"the run whose trials to list"`
}

type trialSummary struct {
	ID           string   `json:"id"`
	ScenarioID   string   `json:"scenario_id"`
	Attempt      uint32   `json:"attempt"`
	StationID    string   `json:"station_id,omitempty"`
	VideoChunks  []string `json:"video_chunk_ids,omitempty"`
	StartedNs    uint64   `json:"started_ns"`
	EndedNs      uint64   `json:"ended_ns"`
	Disposition  string   `json:"disposition"`
	VerdictCount int      `json:"verdict_count"`
}

type listTrialsResult struct {
	Trials []trialSummary `json:"trials"`
}

func (d Deps) listTrials(ctx context.Context, _ *mcpsdk.CallToolRequest, args listTrialsArgs) (*mcpsdk.CallToolResult, listTrialsResult, error) {
	if args.RunID == "" {
		return nil, listTrialsResult{}, fmt.Errorf("run_id is required")
	}
	trials, err := d.Evals.Trials(ctx, args.RunID)
	if err != nil {
		return nil, listTrialsResult{}, err
	}
	out := listTrialsResult{Trials: make([]trialSummary, 0, len(trials))}
	for _, t := range trials {
		out.Trials = append(out.Trials, trialSummary{
			ID: t.Id, ScenarioID: t.ScenarioId, Attempt: t.Attempt, StationID: t.StationId,
			VideoChunks: t.VideoChunkIds, StartedNs: t.StartedNs, EndedNs: t.EndedNs,
			Disposition: dispositionName(t.Outcome), VerdictCount: len(t.Verdicts),
		})
	}
	return nil, out, nil
}

func dispositionName(o *gantryv1.TrialOutcome) string {
	if o == nil {
		return "PENDING"
	}
	switch o.Disposition {
	case gantryv1.Disposition_DISPOSITION_VOID:
		return "VOID"
	case gantryv1.Disposition_DISPOSITION_PASS:
		return "PASS"
	case gantryv1.Disposition_DISPOSITION_FAIL:
		return "FAIL"
	default:
		return "PENDING"
	}
}

// ---- submit_verdict ----

type verdictCheckArg struct {
	Name      string   `json:"name" jsonschema:"check name, e.g. placed or task_time_s"`
	Phase     string   `json:"phase" jsonschema:"precondition | during | outcome"`
	Required  bool     `json:"required" jsonschema:"whether this check gates the disposition"`
	Pass      bool     `json:"pass" jsonschema:"boolean result (for a numeric check, whether value meets the threshold)"`
	Value     float64  `json:"value,omitempty" jsonschema:"optional numeric measurement, e.g. task_time_s"`
	Op        string   `json:"op,omitempty" jsonschema:"optional numeric comparator: <= >= < > == !="`
	Threshold float64  `json:"threshold,omitempty" jsonschema:"optional numeric threshold paired with op"`
	Labels    []string `json:"labels,omitempty" jsonschema:"optional labels, e.g. detected classes"`
}

type submitVerdictArgs struct {
	TrialID         string            `json:"trial_id" jsonschema:"the trial to score"`
	VerifierID      string            `json:"verifier_id" jsonschema:"stable verifier id, e.g. agent-claude"`
	VerifierVersion string            `json:"verifier_version" jsonschema:"pinned verifier version"`
	VerifierDigest  string            `json:"verifier_digest,omitempty" jsonschema:"optional weights/config digest"`
	Notes           string            `json:"notes,omitempty"`
	Checks          []verdictCheckArg `json:"checks" jsonschema:"the checks this verdict asserts"`
}

type submitVerdictResult struct {
	TrialID     string `json:"trial_id"`
	Disposition string `json:"disposition"`
	VerdictID   string `json:"verifier_id"`
}

func (d Deps) submitVerdict(ctx context.Context, req *mcpsdk.CallToolRequest, args submitVerdictArgs) (*mcpsdk.CallToolResult, submitVerdictResult, error) {
	// submit_verdict mutates a trial's disposition — a read-scoped MCP client
	// must not flip a verdict. Gate it on the least-privilege verify scope.
	if err := requireToolScope(req, auth.ScopeVerify); err != nil {
		return nil, submitVerdictResult{}, err
	}
	if args.TrialID == "" || args.VerifierID == "" {
		return nil, submitVerdictResult{}, fmt.Errorf("trial_id and verifier_id are required")
	}
	v := &gantryv1.Verdict{
		VerifierId:      args.VerifierID,
		VerifierVersion: args.VerifierVersion,
		VerifierDigest:  args.VerifierDigest,
		Notes:           args.Notes,
	}
	for _, c := range args.Checks {
		kind := gantryv1.CheckKind_CHECK_KIND_BOOL
		if c.Op != "" {
			kind = gantryv1.CheckKind_CHECK_KIND_NUMERIC
		}
		v.Checks = append(v.Checks, &gantryv1.Check{
			Name: c.Name, Phase: phaseFromName(c.Phase), Required: c.Required, Kind: kind,
			Pass: c.Pass, Value: c.Value, Op: c.Op, Threshold: c.Threshold, Labels: c.Labels,
		})
	}
	t, err := d.Evals.SubmitVerdict(ctx, args.TrialID, v)
	if err != nil {
		return nil, submitVerdictResult{}, err
	}
	return nil, submitVerdictResult{TrialID: t.Id, Disposition: dispositionName(t.Outcome), VerdictID: args.VerifierID}, nil
}

func phaseFromName(s string) gantryv1.Phase {
	switch s {
	case "precondition":
		return gantryv1.Phase_PHASE_PRECONDITION
	case "during":
		return gantryv1.Phase_PHASE_DURING
	default:
		return gantryv1.Phase_PHASE_OUTCOME
	}
}
