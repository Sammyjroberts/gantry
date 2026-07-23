#!/usr/bin/env python3
"""One-command SIM SO-101 release-gating demo - the RFC's canonical example, live.

This is the "gate a model version on a sim SO-101" story end to end, against a
Bench that has EvalService + StationService + IngestService mounted (localhost
needs no token). It:

  1. registers a sim SO-101 station           (tags arm=so101, sim=true)
  2. upserts the "arm-pickplace-sim" suite     (1 scenario, budget 150,
                                                 min_scored 100, gate: success_rate
                                                 non_inferior, margin 0.03)
  3. runs a BASELINE at --baseline-rate (0.90) and PROMOTES it to champion
  4. runs a CANDIDATE at --candidate-rate and GATES it against that champion,
     printing PASS/FAIL + the Wilson interval the server used.

So, today:

    python so101_sim_gate.py --candidate-rate 0.92     # expect PASS  (exit 0)
    python so101_sim_gate.py --candidate-rate 0.70     # expect FAIL  (exit 1)

The candidate step's exit code is the gate result, so this file is itself a CI
step. For the real thing you would swap the sim policy for your trained model
(see gantry_eval.sim_run_trial's docstring and README.md) and call `gantry gate`.

WHY THE DEMO RUNS 600 TRIALS, NOT 150 (the honest statistics)
-------------------------------------------------------------
The authored suite carries the RFC's canonical `trial_budget = 150` /
`min_scored = 100`. But the gate compares the candidate's *Wilson 95% lower
bound* against the champion's rate minus the margin - and at 150 trials that
lower bound sits ~5-6 points below the candidate's point estimate. So a
candidate that is genuinely 2 points better than a 90% champion clears a
3-point non-inferiority margin only about HALF the time at n=150 (Monte-Carlo:
150->48%, 250->68%, 400->83%, 600->96%; a 0.70 candidate fails 100% at every n).
That is the gate working exactly as the RFC intends - "gating on new>=old gates
on noise" - not a bug. To make the headline PASS/FAIL reproducible today the
demo defaults to --budget 600 (~96% reliable). Pass --budget 150 to watch the
gate honestly call a real 2-point gain "within noise". A 0.70 candidate FAILs
at any budget.
"""

from __future__ import annotations

import argparse
import random
import sys

import gantry_eval as ge

SUITE_NAME = "arm-pickplace-sim"
SCENARIO_ID = "pick-red-block"
STATION_ID = "so101-sim-01"
STATION_SELECTOR = "arm=so101,sim=true"


def ensure_station(station: ge.StationClient) -> str:
    """Register (idempotent) the sim SO-101 station and return its id.

    Registering stamps last_seen_ns, so the station reads ONLINE immediately
    (availability is derived from liveness, staleAfter 60s) - which is what makes
    CheckTarget/Lease succeed for a sim rig with no real telemetry heartbeat."""
    st = station.register_station(
        {
            "id": STATION_ID,
            "tags": {"arm": "so101", "sim": "true", "camera": "topdown", "gripper": "parallel"},
            "deviceIds": [STATION_ID],
            "healthJson": '{"sim": true}',
        }
    )
    return st.get("id", STATION_ID)


def ensure_suite(ev: ge.EvalClient, name: str = SUITE_NAME) -> dict:
    """Upsert the arm-pickplace-sim suite (looked up by name, else created). Gate
    is success_rate non_inferior margin 0.03 with a Wilson 95% bound - the RFC's
    release rule. The authored trial_budget/min_scored are the RFC's canonical
    150/100; the runner's --budget controls how many attempts actually run."""
    for s in ev.list_suites():
        if s.get("name") == name:
            return s
    suite = {
        "name": name,
        "subjectKind": "policy",
        "scenarios": [
            {
                "id": SCENARIO_ID,
                "name": "pick red block from bin A",
                "paramsJson": '{"block": "red", "bin": "A"}',
                "trialBudget": 150,
                "minScored": 100,
            }
        ],
        # Gate policy: not meaningfully worse than the champion within 3 points.
        "gateJson": '[{"metric":"success_rate","op":"non_inferior","margin":0.03,"confidence":0.95}]',
        # A per-trial task-time metric (p50) for the report; not gated.
        "metricsJson": '[{"name":"p50_task_time_s","check":"task_time_s","agg":"p50"}]',
    }
    return ev.upsert_suite(suite)


def run_phase(ev, station, ingest, suite_id, label, ref, rate, budget, seed, key, log):
    """Run one phase (baseline or candidate) and return its EvalRun id."""
    candidate = ge.subject_from_ref(ref)
    ev.register_subject(candidate)
    cfg = ge.RunConfig(
        suite_id=suite_id,
        candidate=candidate,
        scenario_id=SCENARIO_ID,
        budget=budget,
        station_selector=STATION_SELECTOR,
        run_idempotency_key=key,
    )
    log(f"\n=== {label}: {ref}  (sim success rate {rate}) ===")
    run = ge.run_suite(ev, station, ingest, cfg, ge.sim_run_trial(rate), rng=random.Random(seed), log=log)
    return run.get("id", "")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--endpoint", default="http://localhost:4780")
    ap.add_argument("--token", default=None, help="gtk_ token (eval/verify scopes); localhost needs none")
    ap.add_argument("--baseline-rate", type=float, default=0.90, help="sim success rate for the champion")
    ap.add_argument("--candidate-rate", type=float, default=0.92, help="sim success rate for the candidate")
    ap.add_argument("--budget", type=int, default=600, help="trials per run; 600 makes a 2pp gain reliably clear the 3pp margin (see module docstring). 150 = the RFC's authored budget, where the gate honestly calls a 2pp gain 'within noise'.")
    ap.add_argument("--seed", type=int, default=1234, help="base RNG seed (baseline uses seed, candidate seed+1)")
    ap.add_argument("--run-tag", default="", help="suite/idempotency suffix. Default: a fresh id per invocation, so each run authors its own suite and bootstraps its own champion (fully repeatable). Pin it to reuse a suite across invocations.")
    ap.add_argument("--skip-baseline", action="store_true", help="reuse the existing champion for --run-tag; gate the candidate only")
    return ap


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    transport = ge.HttpTransport(args.endpoint, args.token)
    ev = ge.EvalClient(transport)
    station = ge.StationClient(transport)
    ingest = ge.IngestClient(transport)
    log = print
    tag = args.run_tag or ("run" + "".join(random.Random().choices("0123456789abcdef", k=6)))
    suite_name = f"{SUITE_NAME}-{tag}"

    log(f"Endpoint: {args.endpoint}")
    ensure_station(station)
    suite = ensure_suite(ev, suite_name)
    suite_id = suite["id"]
    log(f"Station {STATION_ID} registered; suite {suite_name} = {suite_id}")
    if args.budget < 100:
        log(f"NOTE: --budget {args.budget} < suite min_scored (100); the gate will be INCONCLUSIVE.")

    if not args.skip_baseline:
        base_run = run_phase(
            ev, station, ingest, suite_id, "BASELINE",
            f"models://act-pickplace@baseline-{tag}", args.baseline_rate,
            args.budget, args.seed, f"baseline-{tag}", log,
        )
        gate = ev.evaluate_gate(base_run)
        ge_print_gate(gate, log)
        if gate.get("passed"):
            b = ev.promote_baseline(base_run, idempotency_key=f"promote-{base_run}")
            log(f"Promoted baseline: champion rate = {b.get('successRate')}")
        else:
            log("Baseline did not pass its own gate (bootstrap) - cannot promote; aborting.")
            return 2

    cand_run = run_phase(
        ev, station, ingest, suite_id, "CANDIDATE",
        f"models://act-pickplace@candidate-{tag}", args.candidate_rate,
        args.budget, args.seed + 1, f"candidate-{tag}", log,
    )
    gate = ev.evaluate_gate(cand_run)
    ge_print_gate(gate, log)

    passed = gate.get("passed", False)
    log("\n" + ("RELEASE ALLOWED (candidate is not worse than the champion)."
                if passed else
                "RELEASE BLOCKED (candidate failed the gate)."))
    return 0 if passed else 1


def ge_print_gate(gate: dict, log) -> None:
    passed = gate.get("passed", False)
    inconclusive = gate.get("inconclusive", False)
    verdict = "PASS" if passed else ("INCONCLUSIVE" if inconclusive else "FAIL")
    p, f, v = int(gate.get("pass", 0)), int(gate.get("fail", 0)), int(gate.get("void", 0))
    log(f"GATE: {verdict}   (pass={p} fail={f} void={v}, scored={p + f})")
    for c in gate.get("checks", []):
        mark = "ok" if c.get("passed") else "XX"
        log(f"  [{mark}] {c.get('metric')}: {c.get('detail', '')}")


if __name__ == "__main__":
    sys.exit(main())
