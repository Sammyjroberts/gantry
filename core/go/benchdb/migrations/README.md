# Migration standard

**New migrations use goose's timestamp naming: `YYYYMMDDHHMMSS_name.sql`**
(e.g. `20260719141500_eval.sql`). Never sequential numbers.

Why: sequential numbers collide across parallel branches (it happened three
times in one week — eval vs tokens vs sources all claimed 0006/0007). A
timestamp taken when you author the migration is unique by construction, and
the benchdb provider runs with `WithAllowOutofOrder(true)` so a branch merged
second still applies cleanly even if its timestamp predates an already-applied
one.

Rules:
- `0001..0007` are frozen history — never renumber or edit applied migrations.
- One concern per migration; goose Up/Down sections; add the new file to the
  `embedsrcs` in `BUILD.bazel` (gazelle usually handles it).
- Cloud/Postgres uses the same files with a PG provider — keep SQL portable
  (INTEGER ns timestamps, TEXT ids; avoid SQLite-only syntax where possible).
