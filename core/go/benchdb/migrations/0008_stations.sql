-- +goose Up
-- Stations: the unit of hardware checkout (see proto/gantry/v1/station.proto). A
-- station is a tagged bundle of devices+sensors; leases reserve one station at a
-- time so a shared rig supports many concurrent, non-overlapping checkouts.
-- Availability is derived from last_seen_ns at read time and is not stored.
CREATE TABLE stations (
    id              TEXT    PRIMARY KEY,
    bench_host_id   TEXT    NOT NULL DEFAULT '',
    tags_json       TEXT    NOT NULL DEFAULT '{}',
    device_ids_json TEXT    NOT NULL DEFAULT '[]',
    health_json     TEXT    NOT NULL DEFAULT '',
    last_seen_ns    INTEGER NOT NULL DEFAULT 0,
    created_ns      INTEGER NOT NULL
);

-- Leases: a reservation of a station with a TTL. A lease is "active" while
-- released = 0 and expires_ns > now; the service enforces one active lease per
-- station. A non-empty idempotency_key is unique so a retried Lease re-attaches.
CREATE TABLE station_leases (
    id              TEXT    PRIMARY KEY,
    station_id      TEXT    NOT NULL,
    holder          TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT '',
    acquired_ns     INTEGER NOT NULL,
    expires_ns      INTEGER NOT NULL,
    released         INTEGER NOT NULL DEFAULT 0,
    idempotency_key TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_station_leases_station ON station_leases (station_id);
CREATE UNIQUE INDEX idx_station_leases_idem ON station_leases (idempotency_key) WHERE idempotency_key <> '';
-- The exclusivity invariant lives in the schema, not app code: at most one
-- un-released lease per station. A grant releases expired leases first, then
-- inserts under this index — so concurrent grabbers serialize at the DB (one
-- wins, the rest hit a unique violation) on SQLite and Postgres alike.
CREATE UNIQUE INDEX idx_station_leases_active ON station_leases (station_id) WHERE released = 0;

-- +goose Down
DROP INDEX idx_station_leases_idem;
DROP INDEX idx_station_leases_station;
DROP TABLE station_leases;
DROP TABLE stations;
