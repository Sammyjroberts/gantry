-- +goose Up
-- Telemetry sources: connections the Bench itself maintains to pull telemetry in
-- from external publishers — the first kind being a Foxglove WebSocket server
-- (lerobot --display_mode=foxglove, ROS 2 foxglove_bridge). Configured in the UI,
-- persisted here, supervised in-process (connect, decode, map, ingest, reconnect
-- with backoff). See proto/gantry/v1/source.proto and core/go/foxglove.
--
-- id is a generated random hex token (empty on create, like experiments /
-- workspaces). type is "foxglove" today (the column exists so future kinds —
-- mqtt, rtsp — reuse this table). mapping_json is a versioned document owned by
-- the console (a {"profile":"lerobot"} reference or explicit rules) stored
-- opaquely. enabled drives the supervisor: only enabled sources are connected.
-- created_ns is set once on insert and preserved across updates; updated_ns is
-- stamped on every write. This schema is identical on Bench (SQLite) and Cloud
-- (Postgres); only the driver differs.
CREATE TABLE sources (
    id           TEXT    PRIMARY KEY,
    type         TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    url          TEXT    NOT NULL,
    mapping_json TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 0,
    created_ns   INTEGER NOT NULL,
    updated_ns   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE sources;
