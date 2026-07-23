# Gantry release-gating quickstart (sim SO-101)

Gate a model version on a bench the way you gate a PR on tests: run the
candidate N times on a (simulated or real) SO-101, score each attempt, and
**only release if it's not statistically worse than the current champion**. This
directory is the runnable connective tissue on top of Gantry's eval services —
a tiny Connect+JSON client, a reference **sim runner**, and a one-command demo.

Plain Python 3.9+ stdlib. No build, no SDK, no dependency install.

```
examples/eval/
  gantry_eval.py       # Connect+JSON client + reference sim runner (the template)
  so101_sim_gate.py    # one-command sim SO-101 gate demo (baseline -> promote -> gate)
  test_gantry_eval.py  # pure-part tests (no hardware, no network)
  README.md            # this file
```

## What "gating" is

- **Suite** — a reusable test definition: scenarios, a scoring/verifier spec, a
  metric list, and a **gate policy**. Here: `arm-pickplace-sim`.
- **Run** — one execution of a suite against one **candidate** (a versioned
  artifact ref, e.g. `models://act-pickplace@2025.07-rc1`).
- **Trial** — one attempt at a scenario. A trial *is* an experiment (a bracketed
  telemetry range) plus one or more **verdicts** (bundles of phased checks).
- **Disposition** — each trial rolls up to `PASS`, `FAIL`, or `VOID`
  (precondition not met → excluded from the rate, so a mis-staged bench never
  counts as a policy failure).
- **Gate** — compares the candidate's `success_rate` against the champion's
  under a **non-inferiority** rule (Wilson 95% lower bound ≥ champion − margin).
  Pass → releasable; fail/inconclusive → blocked.

## Run the demo today

You need a Bench with `EvalService`, `StationService`, and `IngestService`
mounted (the standard `bench` binary). On `localhost` no token is required.

```bash
# Expect PASS (exit 0): a 0.92 candidate is not worse than a 0.90 champion.
python so101_sim_gate.py --candidate-rate 0.92

# Expect FAIL (exit 1): a 0.70 candidate is clearly worse.
python so101_sim_gate.py --candidate-rate 0.70

# Point at a remote bench / cloud:
python so101_sim_gate.py --endpoint https://bench.example.com --token gtk_... --candidate-rate 0.92
```

Each invocation: registers a sim SO-101 station (`arm=so101, sim=true`), authors
its own `arm-pickplace-sim-<tag>` suite, runs a baseline and promotes it to
champion, then runs the candidate and gates it — printing the Wilson interval the
server used. The candidate step's **exit code is the gate result**, so the file
is itself a CI step. Example tail:

```
GATE: PASS   (pass=554 fail=46 void=0, scored=600)
  [ok] success_rate: Wilson 95% lower bound 0.8992 >= baseline 0.8800 - margin 0.0300 = 0.8500
RELEASE ALLOWED (candidate is not worse than the champion).
```

A full demo drives ~1200 trials (2 × 600) and takes ~60s against a localhost
bench — fast, because the sim policy is instant; real robot trials dominate.

### Why the demo runs 600 trials, not 150 (the honest statistics)

The authored suite carries the RFC's canonical `trial_budget = 150` /
`min_scored = 100`, `margin = 0.03`. But the gate compares the candidate's
**Wilson 95% lower bound** against `champion_rate − margin`, and at 150 trials
that lower bound sits ~5–6 points below the candidate's point estimate. So a
candidate that is genuinely **2 points better** than a 90% champion clears a
**3-point** non-inferiority margin only about half the time at n = 150:

| trials | 0.92 candidate PASS rate | 0.70 candidate PASS rate |
|-------:|-------------------------:|-------------------------:|
| 150    | 48%                      | 0%                       |
| 250    | 68%                      | 0%                       |
| 400    | 83%                      | 0%                       |
| 600    | 96%                      | 0%                       |

That is the gate working exactly as the RFC intends — *"gating on new ≥ old
gates on noise"* — not a bug. The demo defaults to `--budget 600` so the
headline PASS is reproducible. Run `--budget 150` to watch the gate honestly
call a real 2-point gain "within noise". A 0.70 candidate FAILs at any budget.

## How the runner maps to a REAL model (the `run_trial` swap)

The sim's only fiction is one function. In `gantry_eval.py`:

```python
def run_trial(scenario: dict, attempt: int, rng) -> TrialOutcome:
    # SIM default: flip success at --success-rate and emit plausible telemetry.
    ...
```

To gate your trained model, replace it with a function that drives the arm and
reads the result — everything else (station lease, trial bracketing, idempotency
keys, verdict submission, gating math) stays identical:

```python
def run_trial(scenario, attempt, rng) -> ge.TrialOutcome:
    stage_the_bench()                      # home the arm, place blocks
    t0 = time.monotonic()
    invoke_policy(candidate_uri, scenario) # run YOUR ACT/policy on the station's device
    success = read_success_from_camera()   # or from telemetry / a YOLO sidecar
    return ge.TrialOutcome(
        success=success,
        task_time_s=time.monotonic() - t0,
        staged=True,                       # False -> the trial VOIDs (excluded from the rate)
    )

ge.run_suite(ev, station, ingest, cfg, run_trial)   # same loop, real trials
```

The runner publishes each trial's telemetry into that trial's experiment window
(`device_id == station_id`), so if the suite also configures a **telemetry
verifier** (DuckDB over the range) it has data to score — but the authoritative
pass/fail here comes from the `SubmitVerdict` an external "sim-scorer" writes, so
the loop works even with no DuckDB. A vision/LLM verifier is just another
`SubmitVerdict` against the same trial.

## The trial loop (what `run_suite` does)

```
CheckTarget(selector)                 # dry run: enough stations online + free?
Lease(selector) -> station, lease     # StationService reserves the arm (TTL + heartbeat)
StartRun(suite, candidate, key)       # idempotent on key; retries re-attach
for attempt in 0..budget-1:
    OpenTrial(run, scenario, attempt) # natural-key idempotent; starts the experiment
    outcome = run_trial(...)          # <-- your policy
    PublishBatch(station_id, frames)  # telemetry into the trial window
    CloseTrial(trial)                 # end_ns==0 guard -> idempotent
    SubmitVerdict(trial, verdict)     # upsert on (verifier_id, version)
ReleaseLease(lease)
EvaluateGate(run)                     # server computes metrics + Wilson gate
PromoteBaseline(run)                  # optional, idempotent per run
```

Every mutating call carries an idempotency key derived from the run key +
`(scenario, attempt)`, so a retried/resumed run never double-counts a trial.

## Plugging into CI

The demo file is already a gate (exit 0 pass / 1 fail). For a production
pipeline the project ships a `gantry gate` CLI that wraps these same RPCs:

```bash
gantry gate \
  --suite arm-pickplace \
  --candidate models://act-pickplace@$GIT_SHA \
  --baseline latest \
  --target 'arm=so101,camera=topdown' \
  --report junit=report.xml,md=summary.md,json=result.json \
  --idempotency-key $GITHUB_RUN_ID \      # a retried CI job re-attaches, never double-runs
  --promote-on-pass=false
```

- **Exit codes**: `0` pass, non-zero on fail/inconclusive — the "just another CI
  step" contract. Inconclusive (too few scored trials vs `min_scored`) is treated
  as a fail so a thin run never green-lights a release.
- **JUnit / markdown / json** reports for PR comments and artifact upload.
- **Idempotency key** = your CI run id, so retries re-attach to the same run.

Until the `gantry` CLI lands from the parallel branch, `so101_sim_gate.py` and
`gantry_eval.py --gate` give you the same exit-code contract over pure Python.

## Auth

- **localhost**: no token needed (the bench trusts loopback unless started with
  `-require-auth`).
- **remote bench / cloud**: pass `--token gtk_...`. The token needs the
  eval/verify scopes (the ones the eval services enforce) — see the bench's token
  docs. It is sent as `Authorization: Bearer <token>` on every call.

## Tests

```bash
python -m pytest examples/eval -q
```

Pure parts only — no hardware, no live bench: sim rate convergence, idempotency-
key stability, and request shaping asserted against the proto contract (camelCase
fields, 64-bit ints as JSON strings, enum names, the `Value` oneof) via a
recording transport.

## Proto contract this tooling targets

`gantry.v1.EvalService` and `gantry.v1.StationService` over Connect+JSON
(`/<package>.<Service>/<Method>`). Telemetry rides the existing
`gantry.v1.IngestService`. All policy-shaped config (checks, gate, metrics) is
opaque versioned JSON, so the wire contract stays stable as schemas evolve.
