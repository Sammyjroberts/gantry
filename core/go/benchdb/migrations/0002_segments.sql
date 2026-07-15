-- +goose Up
-- Segment index: the SQLite catalog of immutable Parquet segment files written
-- by core/go/segments. Telemetry rows themselves live in the Parquet files (in
-- the blob store), NEVER in SQLite — this table stores only per-segment metadata
-- so a bounded time-range query can find the overlapping segments without
-- opening any file. One row per (device, flushed segment). blob_key is the
-- logical blob.Store key, e.g. "segments/<device>/<start_ns>-<end_ns>.parquet".
CREATE TABLE segments (
    id         TEXT    PRIMARY KEY,
    device_id  TEXT    NOT NULL,
    start_ns   INTEGER NOT NULL,
    end_ns     INTEGER NOT NULL,
    frames     INTEGER NOT NULL,
    bytes      INTEGER NOT NULL,
    blob_key   TEXT    NOT NULL,
    created_ns INTEGER NOT NULL
);
-- Overlap queries filter WHERE end_ns >= t1 AND start_ns <= t2 (optionally per
-- device). Indexing (start_ns, end_ns) lets SQLite range-scan the candidate set.
CREATE INDEX idx_segments_time ON segments (start_ns, end_ns);
CREATE INDEX idx_segments_device_time ON segments (device_id, start_ns, end_ns);

-- Segment writer recovery checkpoint. A single row (id = 1) holds the highest
-- JetStream stream sequence whose frames are durably flushed into a committed
-- segment. On restart the writer resumes consuming at high_water_seq + 1; frames
-- between the last committed segment and a crash are re-read (at-least-once —
-- duplicate frames across a crash boundary are acceptable in v1).
CREATE TABLE segment_state (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    high_water_seq INTEGER NOT NULL DEFAULT 0
);
INSERT INTO segment_state (id, high_water_seq) VALUES (1, 0);

-- +goose Down
DROP TABLE segment_state;
DROP INDEX idx_segments_device_time;
DROP INDEX idx_segments_time;
DROP TABLE segments;
