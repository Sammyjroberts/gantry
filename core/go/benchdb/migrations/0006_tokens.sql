-- +goose Up
-- Access tokens: named, scoped machine credentials for reaching a Bench from
-- anywhere that isn't localhost (localhost is always trusted — plug in and go).
-- See proto/gantry/v1/auth.proto and core/go/auth. The token secret is shown
-- exactly once at creation; only its SHA-256 hash is stored here. id is the
-- public 8-hex-char prefix embedded in the token string (gtk_<id>_<secret>) and
-- is what the verifier looks a row up by before the constant-time hash compare.
-- scopes is a space-separated subset of {ingest, read, operate, admin}. This
-- schema is identical on Bench (SQLite) and Cloud (Postgres); only the driver
-- differs (BLOB→BYTEA there).
CREATE TABLE tokens (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    secret_hash  BLOB    NOT NULL,
    scopes       TEXT    NOT NULL,
    created_ns   INTEGER NOT NULL,
    last_used_ns INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE tokens;
