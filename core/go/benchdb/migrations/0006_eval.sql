-- +goose Up
-- Evals & release gating (see proto/gantry/v1/eval.proto). Metadata only: a
-- Trial references an experiments row (the bracketed telemetry range) rather
-- than copying frames. This schema is the M0 spine — authoring + run/trial
-- lifecycle + verdict ingest; gating, the baseline store, and station leases
-- land in later migrations. Identical on Bench (SQLite) and Cloud (Postgres);
-- only the driver differs.

-- Suites: reusable test definitions. Everything policy-shaped (verifier/checks/
-- combine/metrics/gate) is stored opaquely as console-owned versioned JSON.
CREATE TABLE eval_suites (
    id                   TEXT    PRIMARY KEY,
    name                 TEXT    NOT NULL,
    subject_kind         TEXT    NOT NULL DEFAULT '',
    verifier_config_json TEXT    NOT NULL DEFAULT '',
    checks_json          TEXT    NOT NULL DEFAULT '',
    combine_json         TEXT    NOT NULL DEFAULT '',
    metrics_json         TEXT    NOT NULL DEFAULT '',
    gate_json            TEXT    NOT NULL DEFAULT '',
    created_ns           INTEGER NOT NULL,
    updated_ns           INTEGER NOT NULL
);

-- Scenarios belong to a suite; replaced wholesale on suite upsert. ord preserves
-- authoring order.
CREATE TABLE eval_scenarios (
    suite_id     TEXT    NOT NULL REFERENCES eval_suites (id) ON DELETE CASCADE,
    id           TEXT    NOT NULL,
    ord          INTEGER NOT NULL,
    name         TEXT    NOT NULL,
    params_json  TEXT    NOT NULL DEFAULT '',
    trial_budget INTEGER NOT NULL DEFAULT 0,
    min_scored   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (suite_id, id)
);

-- Subjects: the versioned artifact under test (or a pinned verifier build),
-- content-addressed by digest.
CREATE TABLE eval_subjects (
    digest        TEXT    PRIMARY KEY,
    kind          TEXT    NOT NULL DEFAULT '',
    uri           TEXT    NOT NULL DEFAULT '',
    version       TEXT    NOT NULL DEFAULT '',
    metadata_json TEXT    NOT NULL DEFAULT '',
    created_ns    INTEGER NOT NULL
);

-- Runs: one execution of a suite against a candidate. candidate is stored as a
-- protojson Subject; baseline resolution is a later milestone (baseline_ref kept
-- for forward compat). A non-empty idempotency_key is unique so a retried
-- StartRun re-attaches instead of creating a second run.
CREATE TABLE eval_runs (
    id               TEXT    PRIMARY KEY,
    suite_id         TEXT    NOT NULL,
    candidate_json   TEXT    NOT NULL DEFAULT '',
    baseline_ref     TEXT    NOT NULL DEFAULT '',
    status           INTEGER NOT NULL DEFAULT 0,
    target_selector  TEXT    NOT NULL DEFAULT '',
    replicas         INTEGER NOT NULL DEFAULT 0,
    station_ids_json TEXT    NOT NULL DEFAULT '[]',
    idempotency_key  TEXT    NOT NULL DEFAULT '',
    created_ns       INTEGER NOT NULL,
    started_ns       INTEGER NOT NULL DEFAULT 0,
    ended_ns         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_eval_runs_suite ON eval_runs (suite_id);
CREATE UNIQUE INDEX idx_eval_runs_idem ON eval_runs (idempotency_key) WHERE idempotency_key <> '';

-- Trials: each references an experiments row (experiment_id). The natural key
-- (run_id, scenario_id, attempt) is unique so OpenTrial replay is idempotent.
-- outcome_json holds the derived TrialOutcome once scoring lands (empty on M0).
CREATE TABLE eval_trials (
    id                   TEXT    PRIMARY KEY,
    run_id               TEXT    NOT NULL,
    scenario_id          TEXT    NOT NULL,
    experiment_id        TEXT    NOT NULL DEFAULT '',
    attempt              INTEGER NOT NULL DEFAULT 0,
    seed                 INTEGER NOT NULL DEFAULT 0,
    station_id           TEXT    NOT NULL DEFAULT '',
    video_chunk_ids_json TEXT    NOT NULL DEFAULT '[]',
    outcome_json         TEXT    NOT NULL DEFAULT '',
    started_ns           INTEGER NOT NULL DEFAULT 0,
    ended_ns             INTEGER NOT NULL DEFAULT 0,
    UNIQUE (run_id, scenario_id, attempt)
);
CREATE INDEX idx_eval_trials_run ON eval_trials (run_id);

-- Verdicts: one per (trial, verifier, verifier_version). SubmitVerdict upserts on
-- this key — resubmitting the same build replaces; a new version adds a distinct
-- verdict (re-grade). The full Verdict rides as protojson.
CREATE TABLE eval_verdicts (
    trial_id         TEXT    NOT NULL,
    verifier_id      TEXT    NOT NULL,
    verifier_version TEXT    NOT NULL,
    verdict_json     TEXT    NOT NULL,
    scored_ns        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (trial_id, verifier_id, verifier_version)
);
CREATE INDEX idx_eval_verdicts_trial ON eval_verdicts (trial_id);

-- +goose Down
DROP INDEX idx_eval_verdicts_trial;
DROP TABLE eval_verdicts;
DROP INDEX idx_eval_trials_run;
DROP TABLE eval_trials;
DROP INDEX idx_eval_runs_idem;
DROP INDEX idx_eval_runs_suite;
DROP TABLE eval_runs;
DROP TABLE eval_subjects;
DROP TABLE eval_scenarios;
DROP TABLE eval_suites;
