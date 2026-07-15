-- +goose Up
-- Hardware: the operator-authored identity layer over a telemetry device. A
-- device exists implicitly the moment it emits frames (see core/go/registry); a
-- hardware row is the configured overlay on top — display name, notes, and the
-- evolving JSON configs (3D visualization + console panel defaults) the console
-- owns. device_id is the natural key and matches the telemetry device_id
-- exactly. The JSON blobs are stored opaquely (schemas are versioned inside the
-- documents). This schema is identical on Edge (SQLite) and core (Postgres);
-- only the driver differs.
CREATE TABLE hardware (
    device_id           TEXT    PRIMARY KEY,
    display_name        TEXT    NOT NULL DEFAULT '',
    description         TEXT    NOT NULL DEFAULT '',
    notes               TEXT    NOT NULL DEFAULT '',
    viz_config_json     TEXT    NOT NULL DEFAULT '',
    panel_defaults_json TEXT    NOT NULL DEFAULT '',
    created_ns          INTEGER NOT NULL,
    updated_ns          INTEGER NOT NULL
);

-- +goose Down
DROP TABLE hardware;
