-- +goose Up
-- Release gating (M1): the baseline (champion) store plus the per-run gate
-- result and the baseline class a run was compared within. A baseline is keyed
-- (suite_id, station_class); station_class defaults to '' so a suite with no
-- declared baseline_class_keys has exactly one champion (see RFC 0001 s15).
ALTER TABLE eval_runs ADD COLUMN station_class TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_runs ADD COLUMN gate_json TEXT NOT NULL DEFAULT '';

CREATE TABLE eval_baselines (
    suite_id      TEXT    NOT NULL,
    station_class TEXT    NOT NULL DEFAULT '',
    subject_json  TEXT    NOT NULL DEFAULT '',
    from_run_id   TEXT    NOT NULL DEFAULT '',
    success_rate  REAL    NOT NULL DEFAULT 0,
    promoted_ns   INTEGER NOT NULL,
    PRIMARY KEY (suite_id, station_class)
);

-- +goose Down
DROP TABLE eval_baselines;
ALTER TABLE eval_runs DROP COLUMN gate_json;
ALTER TABLE eval_runs DROP COLUMN station_class;
