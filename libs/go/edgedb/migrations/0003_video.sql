-- +goose Up
-- Video chunks: the catalog for capture-client video. Every row indexes one
-- self-contained ~2s WebM/MP4 chunk stored as a blob (blob_key). The bytes never
-- enter SQLite — only the metadata that lets live-follow and replay resolve which
-- chunks to fetch. Cameras are implicit: a camera "exists" iff it has chunks
-- (distinct camera_id). This schema is identical on Edge (SQLite) and core (PG);
-- only the driver differs.
CREATE TABLE video_chunks (
    id          TEXT    PRIMARY KEY,
    camera_id   TEXT    NOT NULL,
    start_ns    INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    blob_key    TEXT    NOT NULL,
    mime        TEXT    NOT NULL,
    bytes       INTEGER NOT NULL,
    created_ns  INTEGER NOT NULL
);
-- (camera_id, start_ns) serves the two hot reads: time-range listing for one
-- camera, and the per-camera latest-chunk / distinct-camera scan.
CREATE INDEX idx_video_chunks_camera_start ON video_chunks (camera_id, start_ns);

-- +goose Down
DROP INDEX idx_video_chunks_camera_start;
DROP TABLE video_chunks;
