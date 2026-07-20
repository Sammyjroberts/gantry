// Package eval implements the evals & release-gating spine for Gantry: reusable
// test Suites, versioned Subjects under test, EvalRuns, and Trials (see
// proto/gantry/v1/eval.proto). A Trial is bracketed as an experiment (a named
// telemetry range) via the injected ExperimentBracketer, so trials are
// first-class experiments — listable, CSV-exportable, and SQL-queryable — rather
// than a parallel copy of that machinery. Metadata persists in the Bench SQLite
// store (core/go/benchdb); frames are never copied.
//
// This is the M0 spine: authoring + run/trial lifecycle + verdict ingest, built
// for idempotent retry. Scoring (combining checks into a TrialOutcome), gating,
// baselines, and station checkout land in later milestones.
package eval

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrInvalid is a bad request (e.g. empty name, unknown reference).
var ErrInvalid = errors.New("invalid eval request")

// idBytes → 16 hex chars; collision-negligible at bench scale, short enough to
// paste into a URL or filename (matches the experiments convention).
const idBytes = 8

// ExperimentBracketer is the narrow slice of the experiments engine that eval
// needs to bracket each trial as a telemetry range. *experiments.Service
// satisfies it directly; it is an interface here so eval keeps no hard
// dependency on that package and tests can fake it.
type ExperimentBracketer interface {
	// Start begins an experiment now (startNs == 0). name is required.
	Start(ctx context.Context, name, notes, deviceID string, startNs uint64) (*gantryv1.Experiment, error)
	// Stop ends a running experiment now (endNs == 0).
	Stop(ctx context.Context, id string, endNs uint64) (*gantryv1.Experiment, error)
}

// Service is the eval engine. It validates requests, generates ids, enforces
// idempotency, and delegates persistence to the Store and trial bracketing to an
// ExperimentBracketer. now is injectable for deterministic tests.
type Service struct {
	store   *Store
	exp     ExperimentBracketer
	sampler Sampler
	now     func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithSampler wires a telemetry Sampler so trials can be auto-scored on close by
// a suite's telemetry verifier. A nil sampler is ignored (auto-scoring stays
// off, and ScoreTrialTelemetry is a no-op).
func WithSampler(s Sampler) Option {
	return func(sv *Service) {
		if s != nil {
			sv.sampler = s
		}
	}
}

// NewService builds a Service over an already-migrated *sql.DB and an experiment
// bracketer (share the same *experiments.Service the app already runs, so trial
// experiments show up alongside ordinary ones).
func NewService(db *sql.DB, exp ExperimentBracketer, opts ...Option) *Service {
	s := &Service{store: NewStore(db), exp: exp, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Store exposes the underlying store (used by read paths).
func (s *Service) Store() *Store { return s.store }

// ---- suites ----

// UpsertSuite creates (id empty) or updates a suite. name is required. created_ns
// is preserved across updates; updated_ns is stamped every write.
func (s *Service) UpsertSuite(ctx context.Context, in *gantryv1.Suite) (*gantryv1.Suite, error) {
	if in == nil || in.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	nowNs := uint64(s.now().UnixNano())
	out := cloneSuite(in)
	out.UpdatedNs = nowNs
	if out.Id == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		out.Id = id
		out.CreatedNs = nowNs
	} else {
		existing, err := s.store.GetSuite(ctx, out.Id)
		if err != nil {
			return nil, err
		}
		out.CreatedNs = existing.CreatedNs
	}
	if err := s.store.UpsertSuite(ctx, out); err != nil {
		return nil, err
	}
	return s.store.GetSuite(ctx, out.Id)
}

// GetSuite returns one suite by id (ErrNotFound if unknown).
func (s *Service) GetSuite(ctx context.Context, id string) (*gantryv1.Suite, error) {
	return s.store.GetSuite(ctx, id)
}

// ListSuites returns suites newest-first.
func (s *Service) ListSuites(ctx context.Context) ([]*gantryv1.Suite, error) {
	return s.store.ListSuites(ctx)
}

// ---- subjects ----

// RegisterSubject records a subject ref. digest is required (subjects are
// content-addressed); re-registering the same digest is idempotent.
func (s *Service) RegisterSubject(ctx context.Context, in *gantryv1.Subject) (*gantryv1.Subject, error) {
	if in == nil || in.Digest == "" {
		return nil, fmt.Errorf("%w: subject digest is required", ErrInvalid)
	}
	if err := s.store.UpsertSubject(ctx, in, uint64(s.now().UnixNano())); err != nil {
		return nil, err
	}
	return in, nil
}

// ---- runs ----

// StartRun opens a run against a candidate. suite_id must resolve. When
// idempotencyKey is non-empty and a run already exists for it, that run is
// returned unchanged (a retried CI step re-attaches instead of starting a
// second run).
func (s *Service) StartRun(ctx context.Context, suiteID string, candidate *gantryv1.Subject, baselineRef, targetSelector string, replicas uint32, idempotencyKey string) (*gantryv1.EvalRun, error) {
	if suiteID == "" {
		return nil, fmt.Errorf("%w: suite_id is required", ErrInvalid)
	}
	if idempotencyKey != "" {
		if existing, err := s.store.GetRunByIdempotencyKey(ctx, idempotencyKey); err == nil {
			return existing, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if _, err := s.store.GetSuite(ctx, suiteID); err != nil {
		return nil, err // ErrNotFound if the suite is unknown
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	nowNs := uint64(s.now().UnixNano())
	run := &gantryv1.EvalRun{
		Id:             id,
		SuiteId:        suiteID,
		Candidate:      candidate,
		Status:         gantryv1.RunStatus_RUN_STATUS_PENDING,
		TargetSelector: targetSelector,
		Replicas:       replicas,
		CreatedNs:      nowNs,
	}
	if err := s.store.InsertRun(ctx, run, idempotencyKey); err != nil {
		return nil, err
	}
	_ = baselineRef // resolved at gate time in a later milestone
	return s.store.GetRun(ctx, id)
}

// GetRun returns a run and its trials.
func (s *Service) GetRun(ctx context.Context, id string) (*gantryv1.EvalRun, []*gantryv1.Trial, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	trials, err := s.store.ListTrials(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return run, trials, nil
}

// ListRuns returns runs newest-first, optionally filtered to one suite.
func (s *Service) ListRuns(ctx context.Context, suiteID string) ([]*gantryv1.EvalRun, error) {
	return s.store.ListRuns(ctx, suiteID)
}

// Trials returns a run's trials (used by the MCP verifier tools).
func (s *Service) Trials(ctx context.Context, runID string) ([]*gantryv1.Trial, error) {
	return s.store.ListTrials(ctx, runID)
}

// ---- trials ----

// OpenTrial opens (or re-attaches to) a trial for a run/scenario/attempt,
// starting its underlying experiment on first open. The natural key makes replay
// idempotent: a second OpenTrial with the same key returns the existing trial
// without starting a second experiment.
func (s *Service) OpenTrial(ctx context.Context, runID, scenarioID string, attempt uint32, stationID string, seed uint64) (*gantryv1.Trial, error) {
	if runID == "" || scenarioID == "" {
		return nil, fmt.Errorf("%w: run_id and scenario_id are required", ErrInvalid)
	}
	if existing, err := s.store.GetTrialByKey(ctx, runID, scenarioID, attempt); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return nil, err // ErrNotFound if the run is unknown
	}
	name := fmt.Sprintf("trial %s/%s#%d", runID, scenarioID, attempt)
	exp, err := s.exp.Start(ctx, name, "", stationID, 0)
	if err != nil {
		return nil, fmt.Errorf("eval: bracket trial: %w", err)
	}
	trial := &gantryv1.Trial{
		Id:           mustID(),
		RunId:        runID,
		ScenarioId:   scenarioID,
		ExperimentId: exp.Id,
		Attempt:      attempt,
		Seed:         seed,
		StationId:    stationID,
		StartedNs:    exp.StartNs,
	}
	if err := s.store.InsertTrial(ctx, trial); err != nil {
		// A concurrent OpenTrial won the natural-key race: adopt the winner and
		// discard our spare experiment so we don't leak a running range.
		if adopted, gerr := s.store.GetTrialByKey(ctx, runID, scenarioID, attempt); gerr == nil {
			_, _ = s.exp.Stop(ctx, exp.Id, 0)
			return adopted, nil
		}
		return nil, err
	}
	return s.store.GetTrial(ctx, trial.Id)
}

// CloseTrial ends a trial's experiment and stamps its end. Idempotent: closing an
// already-closed trial is a no-op that still appends any new video chunks.
func (s *Service) CloseTrial(ctx context.Context, trialID string, endNs uint64, videoChunkIDs []string) (*gantryv1.Trial, error) {
	if trialID == "" {
		return nil, fmt.Errorf("%w: trial_id is required", ErrInvalid)
	}
	t, err := s.store.GetTrial(ctx, trialID)
	if err != nil {
		return nil, err
	}
	endedNs := endNs
	if t.EndedNs == 0 {
		exp, serr := s.exp.Stop(ctx, t.ExperimentId, endNs)
		switch {
		case serr == nil:
			endedNs = exp.EndNs
		case endedNs == 0:
			endedNs = uint64(s.now().UnixNano())
		}
		if err := s.store.CloseTrial(ctx, trialID, int64(endedNs), videoChunkIDs); err != nil {
			return nil, err
		}
	} else if len(videoChunkIDs) > 0 {
		if err := s.store.CloseTrial(ctx, trialID, int64(t.EndedNs), videoChunkIDs); err != nil {
			return nil, err
		}
	}
	// When a telemetry Sampler is wired and the suite declares telemetry checks,
	// auto-score the just-closed trial (no-op otherwise).
	if s.sampler != nil {
		return s.ScoreTrialTelemetry(ctx, trialID)
	}
	return s.store.GetTrial(ctx, trialID)
}

// SubmitVerdict records a verifier's verdict for a trial. Upserts on
// (verifier_id, verifier_version): resubmitting the same build replaces in place.
func (s *Service) SubmitVerdict(ctx context.Context, trialID string, v *gantryv1.Verdict) (*gantryv1.Trial, error) {
	if trialID == "" {
		return nil, fmt.Errorf("%w: trial_id is required", ErrInvalid)
	}
	if v == nil || v.VerifierId == "" {
		return nil, fmt.Errorf("%w: verifier_id is required", ErrInvalid)
	}
	if _, err := s.store.GetTrial(ctx, trialID); err != nil {
		return nil, err
	}
	if v.ScoredNs == 0 {
		v.ScoredNs = uint64(s.now().UnixNano())
	}
	if err := s.store.UpsertVerdict(ctx, trialID, v); err != nil {
		return nil, err
	}
	// Recompute the trial disposition from all its verdicts under the default
	// combine policy, so the outcome always reflects the current verdict set.
	t, err := s.store.GetTrial(ctx, trialID)
	if err != nil {
		return nil, err
	}
	outcome := deriveOutcome(t.Verdicts)
	if err := s.store.UpdateTrialOutcome(ctx, trialID, outcome); err != nil {
		return nil, err
	}
	t.Outcome = outcome
	return t, nil
}

// ---- gating ----

// EvaluateGate aggregates a run's trial outcomes into metrics and compares the
// candidate against the baseline for its (suite, class) under the suite gate
// policy. It stores the GateResult on the run (status → GATED) and returns it. A
// missing baseline is a bootstrap: baseline-relative checks pass with a noted
// reason so the first qualifying candidate can seed the champion.
func (s *Service) EvaluateGate(ctx context.Context, runID string) (*gantryv1.GateResult, error) {
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrInvalid)
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	suite, err := s.store.GetSuite(ctx, run.SuiteId)
	if err != nil {
		return nil, err
	}
	trials, err := s.store.ListTrials(ctx, runID)
	if err != nil {
		return nil, err
	}
	m, err := aggregate(trials, suite.MetricsJson)
	if err != nil {
		return nil, err
	}
	baseline, err := s.store.GetBaseline(ctx, run.SuiteId, run.StationClass)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		baseline = nil
	}
	minScored := 0
	for _, sc := range suite.Scenarios {
		minScored += int(sc.MinScored)
	}
	res, err := evaluateGate(m, baseline, suite.GateJson, minScored)
	if err != nil {
		return nil, err
	}
	res.Candidate = run.Candidate
	if err := s.store.SetRunGate(ctx, runID, res, gantryv1.RunStatus_RUN_STATUS_GATED); err != nil {
		return nil, err
	}
	return res, nil
}

// PromoteBaseline promotes a passed run's candidate to the champion for its
// (suite, class). Idempotent: promoting a run that is already the champion is a
// no-op. Requires the run to have been gated and passed.
func (s *Service) PromoteBaseline(ctx context.Context, runID, idempotencyKey string) (*gantryv1.Baseline, error) {
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrInvalid)
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Gate == nil {
		return nil, fmt.Errorf("%w: run %s has not been gated", ErrInvalid, runID)
	}
	if !run.Gate.Passed {
		return nil, fmt.Errorf("%w: run %s gate did not pass", ErrInvalid, runID)
	}
	if existing, err := s.store.GetBaseline(ctx, run.SuiteId, run.StationClass); err == nil {
		if existing.FromRunId == runID {
			return existing, nil // already promoted — idempotent no-op
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	scored := int(run.Gate.Pass + run.Gate.Fail)
	rate := 0.0
	if scored > 0 {
		rate = float64(run.Gate.Pass) / float64(scored)
	}
	b := &gantryv1.Baseline{
		SuiteId:      run.SuiteId,
		StationClass: run.StationClass,
		Subject:      run.Candidate,
		FromRunId:    runID,
		SuccessRate:  rate,
		PromotedNs:   uint64(s.now().UnixNano()),
	}
	if err := s.store.UpsertBaseline(ctx, b); err != nil {
		return nil, err
	}
	_ = idempotencyKey // natural idempotency via from_run_id
	return b, nil
}

// GetBaseline returns the champion for a (suite, class), or ErrNotFound.
func (s *Service) GetBaseline(ctx context.Context, suiteID, class string) (*gantryv1.Baseline, error) {
	if suiteID == "" {
		return nil, fmt.Errorf("%w: suite_id is required", ErrInvalid)
	}
	return s.store.GetBaseline(ctx, suiteID, class)
}

// ---- helpers ----

// cloneSuite copies the fields the service owns, so callers' inputs are never
// mutated and stray server-managed fields are re-derived.
func cloneSuite(in *gantryv1.Suite) *gantryv1.Suite {
	return &gantryv1.Suite{
		Id:                 in.Id,
		Name:               in.Name,
		SubjectKind:        in.SubjectKind,
		Scenarios:          in.Scenarios,
		VerifierConfigJson: in.VerifierConfigJson,
		ChecksJson:         in.ChecksJson,
		CombineJson:        in.CombineJson,
		MetricsJson:        in.MetricsJson,
		GateJson:           in.GateJson,
	}
}

// newID returns a short random hex id from crypto/rand.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("eval: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// mustID returns a random id, panicking only if the OS RNG fails (unrecoverable).
func mustID() string {
	id, err := newID()
	if err != nil {
		panic(err)
	}
	return id
}
