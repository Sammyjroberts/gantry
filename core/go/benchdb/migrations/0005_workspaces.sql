-- +goose Up
-- Workspaces: named, persistent bench layouts — the panel grid (charts, state
-- strips, readouts, 3D, cameras, SQL) plus each panel's configuration (see
-- proto/gantry/v1/workspace.proto). id is a generated random hex token (empty on
-- create, like experiments). layout_json is a versioned document owned by the
-- console and stored opaquely (size-capped in the service). created_ns is set
-- once on insert and preserved across updates; updated_ns is stamped on every
-- write. This schema is identical on Bench (SQLite) and Cloud (Postgres); only
-- the driver differs.
CREATE TABLE workspaces (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    layout_json TEXT    NOT NULL DEFAULT '',
    created_ns  INTEGER NOT NULL,
    updated_ns  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE workspaces;
