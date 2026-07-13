-- +goose Up
-- Experiments: named time ranges over the telemetry stream (metadata only; no
-- frames are copied). The range indexes into the stream and, later, the Parquet
-- segment store.
CREATE TABLE experiments (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    notes      TEXT    NOT NULL DEFAULT '',
    device_id  TEXT    NOT NULL DEFAULT '',
    start_ns   INTEGER NOT NULL,
    end_ns     INTEGER NOT NULL DEFAULT 0,
    created_ns INTEGER NOT NULL
);
CREATE INDEX idx_experiments_start_ns ON experiments (start_ns);

-- +goose Down
DROP INDEX idx_experiments_start_ns;
DROP TABLE experiments;
