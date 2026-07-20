# gantry-gate action

Run a [Gantry](../../../README.md) eval **release gate** as a CI step. It evaluates a
candidate against the baseline on a Bench/Cloud endpoint and **exits non-zero when the
gate does not pass**, so a bench gate blocks a merge/release exactly like a unit-test
gate — except the "assertion" is a robot doing a task N times and a verifier scoring it.

## Usage

```yaml
- uses: actions/checkout@v4
- uses: ./.github/actions/gantry-gate
  with:
    endpoint: ${{ secrets.GANTRY_ENDPOINT }}   # bench/cloud base URL
    token: ${{ secrets.GANTRY_TOKEN }}         # scoped TokenService token
    suite: arm-pickplace
    candidate: models://act-pickplace@${{ github.sha }}
    baseline: latest
    target: arm=so101,camera=topdown
    promote-on-pass: ${{ github.ref == 'refs/heads/main' }}
```

See [`../../workflows/eval-gate.yml`](../../workflows/eval-gate.yml) for a runnable template.

## What it does

1. Builds the `gantry` CLI from this repo (`apps/cli/cmd/gantry`).
2. Runs `gantry gate`, keyed by `$GITHUB_RUN_ID` so a re-run **re-attaches** to the same
   run instead of starting a second robot session.
3. Writes the Markdown summary to the job summary, uploads `gantry-gate.xml` (JUnit) and
   `gantry-gate.json` as artifacts.
4. Propagates the CLI exit code — **0 pass, non-zero fail/inconclusive** — as the step
   result, and exposes a `passed` output.

## Inputs

| input | required | default | notes |
|---|---|---|---|
| `endpoint` | yes | — | Bench or Cloud base URL |
| `token` | no | `""` | Scoped token; `operate` to score, add `admin` to promote |
| `suite` | yes | — | Suite id to gate |
| `candidate` | yes | — | Candidate subject digest/ref |
| `baseline` | no | `latest` | Baseline ref to compare against |
| `target` | no | `""` | Station tag selector |
| `promote-on-pass` | no | `false` | Advance the champion on a passing gate |

## Tokens

The token is a `TokenService` credential with one of the bench's scopes
(`ingest`/`read`/`operate`/`verify`/`admin`). Scope it `operate` for PRs (start runs, drive
trials, evaluate the gate) and add `admin` only on the protected branch so a PR can never
promote a baseline. A bring-your-own **verifier** instead uses the least-privilege `verify`
scope — it can submit verdicts but not drive hardware or promote.

## External consumers

Outside this repo, install the CLI instead of building from source:

```sh
go install github.com/Sammyjroberts/gantry/apps/cli/cmd/gantry@latest
```
