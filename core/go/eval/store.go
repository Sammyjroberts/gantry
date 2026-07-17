package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ErrNotFound is returned when a suite/run/trial id does not exist.
var ErrNotFound = errors.New("eval entity not found")

// Store is the persistence layer for evals over the Bench SQLite database. It
// maps rows to and from proto messages and owns no policy: validation, id
// generation, idempotency, and experiment bracketing live in Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB (see core/go/benchdb).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ---- suites ----

// UpsertSuite inserts or updates a suite and replaces its scenarios atomically.
func (s *Store) UpsertSuite(ctx context.Context, su *gantryv1.Suite) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eval: begin suite tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO eval_suites
		   (id, name, subject_kind, verifier_config_json, checks_json, combine_json, metrics_json, gate_json, created_ns, updated_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, subject_kind=excluded.subject_kind,
		   verifier_config_json=excluded.verifier_config_json, checks_json=excluded.checks_json,
		   combine_json=excluded.combine_json, metrics_json=excluded.metrics_json,
		   gate_json=excluded.gate_json, updated_ns=excluded.updated_ns`,
		su.Id, su.Name, su.SubjectKind, su.VerifierConfigJson, su.ChecksJson,
		su.CombineJson, su.MetricsJson, su.GateJson, int64(su.CreatedNs), int64(su.UpdatedNs))
	if err != nil {
		return fmt.Errorf("eval: upsert suite: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM eval_scenarios WHERE suite_id = ?`, su.Id); err != nil {
		return fmt.Errorf("eval: clear scenarios: %w", err)
	}
	for i, sc := range su.Scenarios {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO eval_scenarios (suite_id, id, ord, name, params_json, trial_budget, min_scored)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			su.Id, sc.Id, i, sc.Name, sc.ParamsJson, int64(sc.TrialBudget), int64(sc.MinScored))
		if err != nil {
			return fmt.Errorf("eval: insert scenario: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eval: commit suite: %w", err)
	}
	return nil
}

// GetSuite returns one suite (with scenarios) by id, or ErrNotFound.
func (s *Store) GetSuite(ctx context.Context, id string) (*gantryv1.Suite, error) {
	su := &gantryv1.Suite{}
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, subject_kind, verifier_config_json, checks_json, combine_json, metrics_json, gate_json, created_ns, updated_ns
		 FROM eval_suites WHERE id = ?`, id).
		Scan(&su.Id, &su.Name, &su.SubjectKind, &su.VerifierConfigJson, &su.ChecksJson,
			&su.CombineJson, &su.MetricsJson, &su.GateJson, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get suite: %w", err)
	}
	su.CreatedNs, su.UpdatedNs = uint64(created), uint64(updated)
	scenarios, err := s.scenarios(ctx, id)
	if err != nil {
		return nil, err
	}
	su.Scenarios = scenarios
	return su, nil
}

func (s *Store) scenarios(ctx context.Context, suiteID string) ([]*gantryv1.Scenario, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, params_json, trial_budget, min_scored
		 FROM eval_scenarios WHERE suite_id = ? ORDER BY ord`, suiteID)
	if err != nil {
		return nil, fmt.Errorf("eval: list scenarios: %w", err)
	}
	defer rows.Close()
	var out []*gantryv1.Scenario
	for rows.Next() {
		sc := &gantryv1.Scenario{}
		var budget, minScored int64
		if err := rows.Scan(&sc.Id, &sc.Name, &sc.ParamsJson, &budget, &minScored); err != nil {
			return nil, fmt.Errorf("eval: scan scenario: %w", err)
		}
		sc.TrialBudget, sc.MinScored = uint32(budget), uint32(minScored)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListSuites returns suites (with scenarios) newest-first.
func (s *Store) ListSuites(ctx context.Context) ([]*gantryv1.Suite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM eval_suites ORDER BY created_ns DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("eval: list suites: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eval: scan suite id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*gantryv1.Suite, 0, len(ids))
	for _, id := range ids {
		su, err := s.GetSuite(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, su)
	}
	return out, nil
}

// ---- subjects ----

// UpsertSubject stores a subject keyed by digest (idempotent re-register).
func (s *Store) UpsertSubject(ctx context.Context, sub *gantryv1.Subject, createdNs uint64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO eval_subjects (digest, kind, uri, version, metadata_json, created_ns)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(digest) DO UPDATE SET
		   kind=excluded.kind, uri=excluded.uri, version=excluded.version, metadata_json=excluded.metadata_json`,
		sub.Digest, sub.Kind, sub.Uri, sub.Version, sub.MetadataJson, int64(createdNs))
	if err != nil {
		return fmt.Errorf("eval: upsert subject: %w", err)
	}
	return nil
}

// ---- runs ----

// InsertRun writes a run row.
func (s *Store) InsertRun(ctx context.Context, r *gantryv1.EvalRun, idempotencyKey string) error {
	candidateJSON, err := marshalMsg(r.Candidate)
	if err != nil {
		return err
	}
	stationIDs, err := json.Marshal(r.StationIds)
	if err != nil {
		return fmt.Errorf("eval: marshal station ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO eval_runs
		   (id, suite_id, candidate_json, baseline_ref, status, target_selector, replicas, station_ids_json, idempotency_key, created_ns, started_ns, ended_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Id, r.SuiteId, candidateJSON, "", int64(r.Status), r.TargetSelector,
		int64(r.Replicas), string(stationIDs), idempotencyKey, int64(r.CreatedNs), int64(r.StartedNs), int64(r.EndedNs))
	if err != nil {
		return fmt.Errorf("eval: insert run: %w", err)
	}
	return nil
}

// GetRunByIdempotencyKey returns the run created under key, or ErrNotFound.
func (s *Store) GetRunByIdempotencyKey(ctx context.Context, key string) (*gantryv1.EvalRun, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM eval_runs WHERE idempotency_key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get run by idem: %w", err)
	}
	return s.GetRun(ctx, id)
}

// GetRun returns one run by id, or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id string) (*gantryv1.EvalRun, error) {
	r := &gantryv1.EvalRun{}
	var candidateJSON, stationIDsJSON, baselineRef, gateJSON string
	var status, replicas, created, started, ended int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, suite_id, candidate_json, baseline_ref, status, target_selector, replicas, station_ids_json, station_class, gate_json, created_ns, started_ns, ended_ns
		 FROM eval_runs WHERE id = ?`, id).
		Scan(&r.Id, &r.SuiteId, &candidateJSON, &baselineRef, &status, &r.TargetSelector,
			&replicas, &stationIDsJSON, &r.StationClass, &gateJSON, &created, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get run: %w", err)
	}
	r.Status = gantryv1.RunStatus(status)
	r.Replicas = uint32(replicas)
	r.CreatedNs, r.StartedNs, r.EndedNs = uint64(created), uint64(started), uint64(ended)
	if candidateJSON != "" {
		r.Candidate = &gantryv1.Subject{}
		if err := protojson.Unmarshal([]byte(candidateJSON), r.Candidate); err != nil {
			return nil, fmt.Errorf("eval: unmarshal candidate: %w", err)
		}
	}
	if gateJSON != "" {
		r.Gate = &gantryv1.GateResult{}
		if err := protojson.Unmarshal([]byte(gateJSON), r.Gate); err != nil {
			return nil, fmt.Errorf("eval: unmarshal gate: %w", err)
		}
	}
	if stationIDsJSON != "" {
		if err := json.Unmarshal([]byte(stationIDsJSON), &r.StationIds); err != nil {
			return nil, fmt.Errorf("eval: unmarshal station ids: %w", err)
		}
	}
	return r, nil
}

// ListRuns returns runs newest-first, optionally filtered to one suite.
func (s *Store) ListRuns(ctx context.Context, suiteID string) ([]*gantryv1.EvalRun, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const q = `SELECT id FROM eval_runs`
	if suiteID == "" {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY created_ns DESC, id DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE suite_id = ? ORDER BY created_ns DESC, id DESC`, suiteID)
	}
	if err != nil {
		return nil, fmt.Errorf("eval: list runs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eval: scan run id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*gantryv1.EvalRun, 0, len(ids))
	for _, id := range ids {
		r, err := s.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- trials ----

// InsertTrial writes a trial row.
func (s *Store) InsertTrial(ctx context.Context, t *gantryv1.Trial) error {
	chunks, err := json.Marshal(t.VideoChunkIds)
	if err != nil {
		return fmt.Errorf("eval: marshal chunks: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO eval_trials
		   (id, run_id, scenario_id, experiment_id, attempt, seed, station_id, video_chunk_ids_json, outcome_json, started_ns, ended_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Id, t.RunId, t.ScenarioId, t.ExperimentId, int64(t.Attempt), int64(t.Seed),
		t.StationId, string(chunks), "", int64(t.StartedNs), int64(t.EndedNs))
	if err != nil {
		return fmt.Errorf("eval: insert trial: %w", err)
	}
	return nil
}

// GetTrialByKey returns the trial for a natural key, or ErrNotFound.
func (s *Store) GetTrialByKey(ctx context.Context, runID, scenarioID string, attempt uint32) (*gantryv1.Trial, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM eval_trials WHERE run_id = ? AND scenario_id = ? AND attempt = ?`,
		runID, scenarioID, int64(attempt)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get trial by key: %w", err)
	}
	return s.GetTrial(ctx, id)
}

// GetTrial returns one trial (with verdicts) by id, or ErrNotFound.
func (s *Store) GetTrial(ctx context.Context, id string) (*gantryv1.Trial, error) {
	t := &gantryv1.Trial{}
	var chunksJSON, outcomeJSON string
	var attempt, seed, started, ended int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, scenario_id, experiment_id, attempt, seed, station_id, video_chunk_ids_json, outcome_json, started_ns, ended_ns
		 FROM eval_trials WHERE id = ?`, id).
		Scan(&t.Id, &t.RunId, &t.ScenarioId, &t.ExperimentId, &attempt, &seed, &t.StationId,
			&chunksJSON, &outcomeJSON, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get trial: %w", err)
	}
	t.Attempt, t.Seed = uint32(attempt), uint64(seed)
	t.StartedNs, t.EndedNs = uint64(started), uint64(ended)
	if chunksJSON != "" {
		if err := json.Unmarshal([]byte(chunksJSON), &t.VideoChunkIds); err != nil {
			return nil, fmt.Errorf("eval: unmarshal chunks: %w", err)
		}
	}
	if outcomeJSON != "" {
		t.Outcome = &gantryv1.TrialOutcome{}
		if err := protojson.Unmarshal([]byte(outcomeJSON), t.Outcome); err != nil {
			return nil, fmt.Errorf("eval: unmarshal outcome: %w", err)
		}
	}
	verdicts, err := s.verdicts(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Verdicts = verdicts
	return t, nil
}

// ListTrials returns a run's trials ordered by scenario then attempt.
func (s *Store) ListTrials(ctx context.Context, runID string) ([]*gantryv1.Trial, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM eval_trials WHERE run_id = ? ORDER BY scenario_id, attempt`, runID)
	if err != nil {
		return nil, fmt.Errorf("eval: list trials: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eval: scan trial id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*gantryv1.Trial, 0, len(ids))
	for _, id := range ids {
		t, err := s.GetTrial(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// CloseTrial stamps ended_ns and appends any new video chunks. The ended_ns == 0
// guard makes closing idempotent (a second close affects no row for the stamp).
func (s *Store) CloseTrial(ctx context.Context, id string, endedNs int64, addChunks []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eval: begin close tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE eval_trials SET ended_ns = ? WHERE id = ? AND ended_ns = 0`, endedNs, id); err != nil {
		return fmt.Errorf("eval: close trial: %w", err)
	}
	if len(addChunks) > 0 {
		var chunksJSON string
		if err := tx.QueryRowContext(ctx, `SELECT video_chunk_ids_json FROM eval_trials WHERE id = ?`, id).Scan(&chunksJSON); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("eval: read chunks: %w", err)
		}
		var chunks []string
		if chunksJSON != "" {
			if err := json.Unmarshal([]byte(chunksJSON), &chunks); err != nil {
				return fmt.Errorf("eval: unmarshal chunks: %w", err)
			}
		}
		chunks = append(chunks, addChunks...)
		merged, err := json.Marshal(chunks)
		if err != nil {
			return fmt.Errorf("eval: marshal chunks: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE eval_trials SET video_chunk_ids_json = ? WHERE id = ?`, string(merged), id); err != nil {
			return fmt.Errorf("eval: update chunks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eval: commit close: %w", err)
	}
	return nil
}

// ---- verdicts ----

// UpsertVerdict inserts or replaces a verdict on (trial_id, verifier_id,
// verifier_version).
func (s *Store) UpsertVerdict(ctx context.Context, trialID string, v *gantryv1.Verdict) error {
	blob, err := marshalMsg(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO eval_verdicts (trial_id, verifier_id, verifier_version, verdict_json, scored_ns)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(trial_id, verifier_id, verifier_version) DO UPDATE SET
		   verdict_json=excluded.verdict_json, scored_ns=excluded.scored_ns`,
		trialID, v.VerifierId, v.VerifierVersion, blob, int64(v.ScoredNs))
	if err != nil {
		return fmt.Errorf("eval: upsert verdict: %w", err)
	}
	return nil
}

func (s *Store) verdicts(ctx context.Context, trialID string) ([]*gantryv1.Verdict, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT verdict_json FROM eval_verdicts WHERE trial_id = ? ORDER BY verifier_id, verifier_version`, trialID)
	if err != nil {
		return nil, fmt.Errorf("eval: list verdicts: %w", err)
	}
	defer rows.Close()
	var out []*gantryv1.Verdict
	for rows.Next() {
		var blob string
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("eval: scan verdict: %w", err)
		}
		v := &gantryv1.Verdict{}
		if err := protojson.Unmarshal([]byte(blob), v); err != nil {
			return nil, fmt.Errorf("eval: unmarshal verdict: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateTrialOutcome stores the derived TrialOutcome for a trial.
func (s *Store) UpdateTrialOutcome(ctx context.Context, trialID string, outcome *gantryv1.TrialOutcome) error {
	blob, err := marshalMsg(outcome)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE eval_trials SET outcome_json = ? WHERE id = ?`, blob, trialID)
	if err != nil {
		return fmt.Errorf("eval: update outcome: %w", err)
	}
	return affectedOrNotFound(res)
}

// SetRunGate stores a run's gate result and status.
func (s *Store) SetRunGate(ctx context.Context, runID string, gate *gantryv1.GateResult, status gantryv1.RunStatus) error {
	blob, err := marshalMsg(gate)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE eval_runs SET gate_json = ?, status = ? WHERE id = ?`, blob, int64(status), runID)
	if err != nil {
		return fmt.Errorf("eval: set run gate: %w", err)
	}
	return affectedOrNotFound(res)
}

// UpsertBaseline sets the champion for a (suite_id, station_class).
func (s *Store) UpsertBaseline(ctx context.Context, b *gantryv1.Baseline) error {
	subjectJSON, err := marshalMsg(b.Subject)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO eval_baselines (suite_id, station_class, subject_json, from_run_id, success_rate, promoted_ns)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(suite_id, station_class) DO UPDATE SET
		   subject_json=excluded.subject_json, from_run_id=excluded.from_run_id,
		   success_rate=excluded.success_rate, promoted_ns=excluded.promoted_ns`,
		b.SuiteId, b.StationClass, subjectJSON, b.FromRunId, b.SuccessRate, int64(b.PromotedNs))
	if err != nil {
		return fmt.Errorf("eval: upsert baseline: %w", err)
	}
	return nil
}

// GetBaseline returns the champion for a (suite_id, station_class), or ErrNotFound.
func (s *Store) GetBaseline(ctx context.Context, suiteID, class string) (*gantryv1.Baseline, error) {
	b := &gantryv1.Baseline{SuiteId: suiteID, StationClass: class}
	var subjectJSON string
	var promoted int64
	err := s.db.QueryRowContext(ctx,
		`SELECT subject_json, from_run_id, success_rate, promoted_ns
		 FROM eval_baselines WHERE suite_id = ? AND station_class = ?`, suiteID, class).
		Scan(&subjectJSON, &b.FromRunId, &b.SuccessRate, &promoted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval: get baseline: %w", err)
	}
	b.PromotedNs = uint64(promoted)
	if subjectJSON != "" {
		b.Subject = &gantryv1.Subject{}
		if err := protojson.Unmarshal([]byte(subjectJSON), b.Subject); err != nil {
			return nil, fmt.Errorf("eval: unmarshal baseline subject: %w", err)
		}
	}
	return b, nil
}

// affectedOrNotFound maps a zero-rows-affected result to ErrNotFound.
func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("eval: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// marshalMsg renders a proto message to a protojson string ("" for nil).
func marshalMsg(m proto.Message) (string, error) {
	if m == nil || !m.ProtoReflect().IsValid() {
		return "", nil
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("eval: marshal %T: %w", m, err)
	}
	return string(b), nil
}
