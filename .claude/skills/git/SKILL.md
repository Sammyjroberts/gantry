---
name: git
description: >-
  Conventional Commits rules for the Gantry monorepo. Load this ANY time a commit
  is being created, a branch or PR is named, or a commit message is written or
  reviewed in this repo — by a human or a Claude session. Defines the allowed
  types, the product/area scope map, breaking-change rules, and the required
  Co-Authored-By trailer so release-please (manifest mode) can cut per-product releases.
---

# Gantry commit conventions

Every commit MUST be a valid Conventional Commit. This is enforced by the
`.githooks/commit-msg` hook (opt-in via `just setup`) and is what lets
release-please derive per-product version bumps later.

## Header format

```
<type>(<scope>)!: <description>
```

- `type` required, `scope` optional (but preferred), `!` only for breaking changes.
- Header ≤ 72 chars. Imperative mood ("add", not "adds"/"added"). Lowercase after
  the colon. No trailing period.
- Description says WHAT changed at a glance; the body says WHY.

## Types (pick the dominant intent)

- `feat` — new user-visible capability (drives a minor bump).
- `fix` — bug fix in existing behavior (drives a patch bump).
- `docs` — documentation only (README, ARCHITECTURE, ADRs, this skill).
- `refactor` — code change that neither fixes a bug nor adds a feature.
- `perf` — change made specifically to improve performance.
- `test` — add or correct tests only.
- `build` — build system, deps, codegen, or toolchain (go.mod, Cargo, pnpm, buf).
- `ci` — CI config and workflows (`.github/**`).
- `chore` — maintenance with no src/test/build impact (gitignore, housekeeping).

## Scopes = product/area map

Use exactly one of these; never invent a scope silently — add it to this list first.

| scope | area |
|---|---|
| `bench` | `apps/bench` (Go single binary) |
| `cloud` | `apps/cloud` (ingestd/queryd/controld) |
| `web` | `apps/web` (TS/React console) |
| `cli` | `apps/cli` (the `gantry` CLI) |
| `sdk` | `sdk/` (Rust Edge SDK) |
| `proto` | `proto/gantry/v1` contracts |
| `core-go` | `core/go` shared engine |
| `core-ts` | `core/ts` shared web packages |
| `deploy` | `deploy/` compose/terraform/helm |
| `bazel` | Bazel/bzlmod wiring, BUILD files |
| `docs` | `docs/` architecture + ADRs |
| `skills` | `.claude/skills` |
| `examples` | `examples/` (SO-101 kit, eval runners, adapters demos) |

Multi-area change: pick the **dominant** scope, or omit the scope entirely
(`feat: ...`). Do not chain scopes.

## Breaking changes

Mark with `!` after the type/scope AND a `BREAKING CHANGE:` footer explaining the
migration. Breaking = a bump-major event per affected product scope. Usual suspects:

- `proto/` contract changes (field renumber/removal, RPC signature change).
- `sdk` public-API changes (customer-facing Connect surface).

## Body & footers

- Body: wrap ~72 cols, explain WHY and any non-obvious tradeoffs — not a restatement
  of the diff.
- Footers: `Refs: #123`, `BREAKING CHANGE: <what + migration>`.
- **Claude sessions MUST end the message with the harness trailer** (this is required
  by the repo, separate from Conventional Commits):

```
feat(sdk)!: switch spool sink to length-prefixed framing

Raw framed transport needs a self-describing record boundary so a CCSDS
mapping can slot in later without touching the data model.

BREAKING CHANGE: SpoolSink wire format changed; agents older than v0.4 must
be rebuilt against the new proto. Refs: #142

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## GOOD examples (this repo's domains)

- `feat(sdk): add spool sink with drop-oldest ring buffer`
- `fix(bench): flush stream-open response before first frame`
- `perf(core-go): pre-aggregate segment rollups for zoomed-out plots`
- `refactor(web): extract ring-buffer timeseries into core-ts`
- `build(bazel): pin rules_go 0.61.1 for Go 1.26 toolchain`
- `docs(proto): document last-per-subject replay semantics`

## BAD examples (and the fix)

- `Fixed a bug in bench` → `fix(bench): stop dropping frames on reconnect`
  (type lowercase, imperative, no vague "a bug").
- `feat(engine): new query path` → scope `engine` is not in the map; use
  `feat(core-go): add [t1,t2] segment query path`.
- `update stuff` → missing type/scope/intent; e.g. `chore: bump gitignore for bazel outputs`.

## Release mapping (future release-please manifest mode)

Per product scope: `feat` → **minor**, `fix`/`perf` → **patch**,
any `!` / `BREAKING CHANGE:` → **major**. `docs`/`test`/`build`/`ci`/`chore`/`refactor`
do not bump versions. Accurate type + scope is what makes automated releases correct.
