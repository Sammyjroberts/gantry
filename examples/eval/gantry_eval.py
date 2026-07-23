#!/usr/bin/env python3
"""Gantry eval / release-gating: a Connect+JSON client and a reference sim runner.

The eval services (EvalService, StationService) ship the *scoring engine* - they
score attempts and compute the release gate - but nothing drives the trials or
authors suites except raw RPC. This file is that missing connective tissue:

  * A tiny Connect+JSON client (stdlib urllib only - no build, no SDK), talking
    the stable proto/HTTP contract the way examples/so101/so101_bridge.py talks
    to IngestService. Every mutating call carries an idempotency key.
  * A reference **sim runner** that, given a suite + candidate + station, drives
    the whole trial loop the RFC (sec.8) describes:
        CheckTarget -> Lease -> for attempt in 0..budget:
            OpenTrial -> run_trial(scenario, attempt) -> publish SO-101
            telemetry into the trial's experiment window -> CloseTrial ->
            SubmitVerdict(from the sim outcome)
        -> EvaluateGate -> (PromoteBaseline) -> ReleaseLease

`run_trial` is the pluggable seam. The SIM default flips success at a
`--success-rate` you pass and emits plausible SO-101 telemetry so a
telemetry-verifier *could* score it. To gate your REAL trained model, swap
`run_trial` for a function that invokes your policy on the arm and reads success
from the camera/telemetry - everything else (bracketing, idempotency, gating)
stays identical. See README.md.

Connect/JSON facts this client relies on (verified against the PR-branch proto
and the generated ConnectRPC handlers):
  * RPC path is `/<package>.<Service>/<Method>`, e.g.
    `/gantry.v1.EvalService/StartRun`, `/gantry.v1.StationService/Lease`,
    `/gantry.v1.IngestService/PublishBatch`.
  * JSON field names are lowerCamelCase (protojson): suiteId, idempotencyKey.
  * 64-bit ints (uint64/fixed64) serialize as JSON *strings*: seed, timestampNs,
    createdNs. 32-bit ints (attempt, trialBudget, replicas) are plain numbers.
  * Enums serialize as their names: "PHASE_OUTCOME", "CHECK_KIND_BOOL",
    "DISPOSITION_PASS". protojson accepts the name on input and emits it on output.
  * The Value oneof is `{"f64": <number>}`.

Dependency: none (Python 3.9+ stdlib). Run `python gantry_eval.py --help`.
"""

from __future__ import annotations

import argparse
import json
import random
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import Callable, Dict, List, Optional

# SO-101 joints, matching examples/so101/so101_bridge.py (LeRobot convention).
JOINTS = [
    "shoulder_pan",
    "shoulder_lift",
    "elbow_flex",
    "wrist_flex",
    "wrist_roll",
    "gripper",
]


# ---------------------------------------------------------------------------
# Transport: a thin Connect+JSON POST seam. HttpTransport talks to a real bench;
# RecordingTransport (used by tests) captures request bodies with no network.
# ---------------------------------------------------------------------------


class ConnectError(RuntimeError):
    """A non-2xx Connect response. Carries the Connect error code + message."""

    def __init__(self, path: str, status: int, code: str, message: str):
        self.path = path
        self.status = status
        self.code = code
        self.message = message
        super().__init__(f"{path} -> {status} {code}: {message}")


class Transport:
    """POST a JSON body to a Connect method path, return the decoded response."""

    def post(self, path: str, body: dict) -> dict:  # pragma: no cover - interface
        raise NotImplementedError


class HttpTransport(Transport):
    """Connect+JSON over plain HTTP (ConnectRPC accepts JSON POSTs)."""

    def __init__(self, endpoint: str, token: Optional[str] = None, timeout: float = 15.0):
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.timeout = timeout

    def post(self, path: str, body: dict) -> dict:
        req = urllib.request.Request(
            f"{self.endpoint}/{path.lstrip('/')}",
            data=json.dumps(body).encode(),
            headers={"content-type": "application/json"},
            method="POST",
        )
        if self.token:
            req.add_header("authorization", f"Bearer {self.token}")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", "replace")
            code, message = "unknown", raw
            try:
                doc = json.loads(raw)
                code = doc.get("code", code)
                message = doc.get("message", message)
            except Exception:
                pass
            raise ConnectError(path, e.code, code, message) from None


class RecordingTransport(Transport):
    """Test double: records (path, body) and returns canned responses."""

    def __init__(self, responses: Optional[Dict[str, dict]] = None):
        self.calls: List[tuple] = []
        self.responses = responses or {}

    def post(self, path: str, body: dict) -> dict:
        self.calls.append((path, body))
        resp = self.responses.get(path, {})
        return resp(body) if callable(resp) else resp


# ---------------------------------------------------------------------------
# Typed clients over the transport. Each method builds a protojson body and
# returns the decoded response dict (camelCase, 64-bit ints as strings).
# ---------------------------------------------------------------------------


def _u64(v) -> str:
    """Render a 64-bit int as the JSON string protojson expects."""
    return str(int(v))


class EvalClient:
    """Client for gantry.v1.EvalService."""

    SERVICE = "gantry.v1.EvalService"

    def __init__(self, transport: Transport):
        self.t = transport

    def _call(self, method: str, body: dict) -> dict:
        return self.t.post(f"/{self.SERVICE}/{method}", body)

    # ---- authoring ----

    def upsert_suite(self, suite: dict) -> dict:
        return self._call("UpsertSuite", {"suite": suite}).get("suite", {})

    def get_suite(self, suite_id: str) -> dict:
        return self._call("GetSuite", {"id": suite_id}).get("suite", {})

    def list_suites(self) -> List[dict]:
        return self._call("ListSuites", {}).get("suites", [])

    def register_subject(self, subject: dict) -> dict:
        return self._call("RegisterSubject", {"subject": subject}).get("subject", {})

    # ---- runs ----

    def start_run(
        self,
        suite_id: str,
        candidate: dict,
        target_selector: str = "",
        replicas: int = 1,
        baseline_ref: str = "latest",
        idempotency_key: str = "",
    ) -> dict:
        body = {
            "suiteId": suite_id,
            "candidate": candidate,
            "baselineRef": baseline_ref,
            "targetSelector": target_selector,
            "replicas": replicas,
            "idempotencyKey": idempotency_key,
        }
        return self._call("StartRun", body).get("run", {})

    def get_run(self, run_id: str) -> dict:
        """Returns {'run': {...}, 'trials': [...]}."""
        return self._call("GetRun", {"id": run_id})

    # ---- trials ----

    def open_trial(
        self,
        run_id: str,
        scenario_id: str,
        attempt: int,
        station_id: str = "",
        seed: int = 0,
        idempotency_key: str = "",
    ) -> dict:
        body = {
            "runId": run_id,
            "scenarioId": scenario_id,
            "attempt": attempt,
            "stationId": station_id,
            "seed": _u64(seed),
            "idempotencyKey": idempotency_key,
        }
        return self._call("OpenTrial", body).get("trial", {})

    def close_trial(self, trial_id: str, end_ns: int = 0, video_chunk_ids: Optional[List[str]] = None) -> dict:
        body = {"trialId": trial_id, "endNs": _u64(end_ns), "videoChunkIds": video_chunk_ids or []}
        return self._call("CloseTrial", body).get("trial", {})

    def submit_verdict(self, trial_id: str, verdict: dict, idempotency_key: str = "") -> dict:
        body = {"trialId": trial_id, "verdict": verdict, "idempotencyKey": idempotency_key}
        return self._call("SubmitVerdict", body).get("trial", {})

    # ---- gating ----

    def evaluate_gate(self, run_id: str) -> dict:
        return self._call("EvaluateGate", {"runId": run_id}).get("result", {})

    def promote_baseline(self, run_id: str, idempotency_key: str = "") -> dict:
        body = {"runId": run_id, "idempotencyKey": idempotency_key}
        return self._call("PromoteBaseline", body).get("baseline", {})

    def get_baseline(self, suite_id: str, station_class: str = "") -> dict:
        return self._call("GetBaseline", {"suiteId": suite_id, "stationClass": station_class}).get("baseline", {})


class StationClient:
    """Client for gantry.v1.StationService."""

    SERVICE = "gantry.v1.StationService"

    def __init__(self, transport: Transport):
        self.t = transport

    def _call(self, method: str, body: dict) -> dict:
        return self.t.post(f"/{self.SERVICE}/{method}", body)

    def register_station(self, station: dict) -> dict:
        return self._call("RegisterStation", {"station": station}).get("station", {})

    def list_stations(self, selector: str = "") -> List[dict]:
        return self._call("ListStations", {"selector": selector}).get("stations", [])

    def check_target(self, selector: str, replicas: int = 1, suite_id: str = "") -> dict:
        return self._call("CheckTarget", {"selector": selector, "replicas": replicas, "suiteId": suite_id})

    def lease(
        self,
        selector: str,
        holder: str,
        replicas: int = 1,
        reason: str = "",
        ttl_seconds: int = 0,
        idempotency_key: str = "",
    ) -> dict:
        """Returns {'leases': [...], 'stations': [...]}."""
        body = {
            "selector": selector,
            "replicas": replicas,
            "holder": holder,
            "reason": reason,
            "ttlSeconds": ttl_seconds,
            "idempotencyKey": idempotency_key,
        }
        return self._call("Lease", body)

    def renew_lease(self, lease_id: str, ttl_seconds: int = 0) -> dict:
        return self._call("RenewLease", {"leaseId": lease_id, "ttlSeconds": ttl_seconds}).get("lease", {})

    def release_lease(self, lease_id: str) -> dict:
        return self._call("ReleaseLease", {"leaseId": lease_id})


class IngestClient:
    """Client for gantry.v1.IngestService (the telemetry data plane)."""

    SERVICE = "gantry.v1.IngestService"

    def __init__(self, transport: Transport):
        self.t = transport
        self._sequence = 0

    def _call(self, method: str, body: dict) -> dict:
        return self.t.post(f"/{self.SERVICE}/{method}", body)

    def register_channels(self, device_id: str, channels: List[dict]) -> dict:
        return self._call("RegisterChannels", {"deviceId": device_id, "channels": channels})

    def publish(self, device_id: str, frames: List[dict]) -> dict:
        self._sequence += 1
        batch = {"deviceId": device_id, "sequence": _u64(self._sequence), "frames": frames}
        return self._call("PublishBatch", {"batch": batch})


# ---------------------------------------------------------------------------
# The pluggable policy seam and the SIM default.
# ---------------------------------------------------------------------------


@dataclass
class TrialOutcome:
    """What one attempt produced. The authoritative pass/fail the sim-scorer
    verifier submits; a real runner fills this from the camera/telemetry."""

    success: bool
    task_time_s: float = 0.0
    # Optional precondition: was the bench staged before the attempt? A False
    # here VOIDs the trial (excluded from the success rate) rather than failing.
    staged: bool = True
    # Per-joint position samples emitted during the attempt (for telemetry).
    telemetry: Dict[str, float] = field(default_factory=dict)
    detail: str = ""


# A run_trial is: (scenario_dict, attempt_int, rng) -> TrialOutcome
RunTrial = Callable[[dict, int, random.Random], TrialOutcome]


def sim_run_trial(success_rate: float, stage_fail_rate: float = 0.0) -> RunTrial:
    """Build a SIM run_trial that flips success at `success_rate`.

    This is the template the operator replaces. A real runner instead:
      1. stages/homes the arm, confirms the bench is ready (-> staged),
      2. invokes the trained policy on the station's device(s) for the scenario,
      3. reads success from the top-down camera / telemetry (-> success),
      4. records elapsed wall time (-> task_time_s).
    Everything else in the loop (bracketing, idempotency, gating) is unchanged.
    """

    def _run(scenario: dict, attempt: int, rng: random.Random) -> TrialOutcome:
        staged = rng.random() >= stage_fail_rate
        # Plausible SO-101 joint positions (deg) + a task-time draw.
        base = {j: rng.uniform(-90, 90) for j in JOINTS}
        if not staged:
            return TrialOutcome(success=False, staged=False, telemetry=base, detail="bench not staged -> void")
        success = rng.random() < success_rate
        # Successful picks are a touch faster; failures drag/time out.
        task_time_s = rng.gauss(18.0 if success else 26.0, 3.0)
        task_time_s = max(4.0, task_time_s)
        base["gripper"] = 55.0 if success else rng.uniform(0, 20)  # gripper % closed on a grasp
        return TrialOutcome(
            success=success,
            task_time_s=round(task_time_s, 2),
            staged=True,
            telemetry=base,
            detail="pick-place succeeded" if success else "block not in slot",
        )

    return _run


# ---------------------------------------------------------------------------
# Verdict + telemetry construction (pure - unit tested).
# ---------------------------------------------------------------------------


def build_verdict(
    outcome: TrialOutcome,
    verifier_id: str = "sim-scorer",
    verifier_version: str = "0.1.0",
    scored_ns: int = 0,
    evidence_chunk_ids: Optional[List[str]] = None,
) -> dict:
    """Turn a sim TrialOutcome into a SubmitVerdict payload.

    Three checks, matching the RFC's phased model (sec.7):
      * space_ready   PRECONDITION, required, BOOL   -> a failed one VOIDs the trial
      * placed        OUTCOME,      required, BOOL    -> decides PASS/FAIL
      * task_time_s   OUTCOME, not required, NUMERIC  -> surfaces a per-trial metric
    """
    checks = [
        {
            "name": "space_ready",
            "phase": "PHASE_PRECONDITION",
            "required": True,
            "kind": "CHECK_KIND_BOOL",
            "pass": bool(outcome.staged),
            "detail": "bench staged / arm homed" if outcome.staged else outcome.detail,
        },
        {
            "name": "placed",
            "phase": "PHASE_OUTCOME",
            "required": True,
            "kind": "CHECK_KIND_BOOL",
            "pass": bool(outcome.success),
            "detail": outcome.detail,
        },
        {
            "name": "task_time_s",
            "phase": "PHASE_OUTCOME",
            "required": False,
            "kind": "CHECK_KIND_NUMERIC",
            "value": float(outcome.task_time_s),
            "op": "<=",
            "threshold": 30.0,
        },
    ]
    return {
        "verifierId": verifier_id,
        "verifierVersion": verifier_version,
        "verifierDigest": f"sim:{verifier_version}",
        "scoredNs": _u64(scored_ns or time.time_ns()),
        "checks": checks,
        "notes": outcome.detail,
    }


def so101_channels() -> List[dict]:
    """ChannelInfo list for the sim SO-101 device (one 'pos' per joint + task)."""
    ch = [{"name": "pos", "kind": "VALUE_KIND_F64", "unit": "deg", "packet": j} for j in JOINTS]
    ch.append({"name": "task_time_s", "kind": "VALUE_KIND_F64", "unit": "s", "packet": "task"})
    ch.append({"name": "placed", "kind": "VALUE_KIND_F64", "unit": "bool", "packet": "task"})
    return ch


def telemetry_frames(outcome: TrialOutcome, t_ns: int) -> List[dict]:
    """Frames for one attempt: a joint-position snapshot + task outcome markers.

    Published under device_id == station_id, inside the OpenTrial/CloseTrial
    window, so they land in the trial's experiment range where a telemetry
    verifier (DuckDB over the range) could score them."""
    frames = []
    for joint, pos in outcome.telemetry.items():
        frames.append({"channel": "pos", "packet": joint, "timestampNs": _u64(t_ns), "value": {"f64": float(pos)}})
    frames.append({"channel": "task_time_s", "packet": "task", "timestampNs": _u64(t_ns), "value": {"f64": float(outcome.task_time_s)}})
    frames.append({"channel": "placed", "packet": "task", "timestampNs": _u64(t_ns), "value": {"f64": 1.0 if outcome.success else 0.0}})
    return frames


# ---------------------------------------------------------------------------
# The reference runner: drive a whole run's trial loop.
# ---------------------------------------------------------------------------


@dataclass
class RunConfig:
    suite_id: str
    candidate: dict
    scenario_id: str
    budget: int
    station_selector: str = "arm=so101,sim=true"
    baseline_ref: str = "latest"
    run_idempotency_key: str = ""
    ttl_seconds: int = 3600
    lease: bool = True
    publish_telemetry: bool = True
    verifier_version: str = "0.1.0"


def _selector_first_station(station: StationClient, selector: str) -> Optional[str]:
    stations = station.list_stations(selector)
    return stations[0]["id"] if stations else None


def run_suite(
    ev: EvalClient,
    station: Optional[StationClient],
    ingest: Optional[IngestClient],
    cfg: RunConfig,
    run_trial: RunTrial,
    rng: Optional[random.Random] = None,
    log: Callable[[str], None] = lambda m: None,
) -> dict:
    """Execute one run end to end and return the EvalRun (with its gate populated
    once EvaluateGate runs). Mirrors the RFC sec.8 orchestration.

    Leasing is done here via StationService because StartRun does NOT lease in
    this milestone (it only records target_selector/replicas metadata - verified
    against the PR-branch eval service). So a runner that wants a reserved
    station must Lease it itself; that is exactly what this does.
    """
    rng = rng or random.Random()
    run_key = cfg.run_idempotency_key or f"run-{int(time.time())}-{rng.randrange(1<<30)}"

    lease_id = ""
    station_id = ""
    if station is not None and cfg.lease:
        chk = station.check_target(cfg.station_selector, replicas=1, suite_id=cfg.suite_id)
        log(f"CheckTarget: {chk.get('detail', '')}")
        if not chk.get("ok"):
            # Not fatal for the sim: proceed unleased so the demo still runs, but
            # say so loudly. A real CI gate would exit non-zero here.
            log("  target not satisfiable; proceeding without a lease (sim).")
        else:
            res = station.lease(
                cfg.station_selector,
                holder=run_key,
                replicas=1,
                reason="eval run",
                ttl_seconds=cfg.ttl_seconds,
                idempotency_key=f"lease-{run_key}",
            )
            leases = res.get("leases", [])
            if leases:
                lease_id = leases[0]["id"]
                station_id = leases[0]["stationId"]
                log(f"Leased station {station_id} (lease {lease_id}).")

    if not station_id and station is not None:
        station_id = _selector_first_station(station, cfg.station_selector) or ""

    # Start (or re-attach to) the run.
    run = ev.start_run(
        cfg.suite_id,
        cfg.candidate,
        target_selector=cfg.station_selector,
        replicas=1,
        baseline_ref=cfg.baseline_ref,
        idempotency_key=run_key,
    )
    run_id = run["id"]
    log(f"Run {run_id} started (candidate {cfg.candidate.get('version', '?')}).")

    if ingest is not None and cfg.publish_telemetry and station_id:
        ingest.register_channels(station_id, so101_channels())

    scenario = {"id": cfg.scenario_id}
    passed = failed = void = 0
    try:
        for attempt in range(cfg.budget):
            trial = ev.open_trial(
                run_id,
                cfg.scenario_id,
                attempt,
                station_id=station_id,
                seed=rng.randrange(1 << 32),
                idempotency_key=f"{run_key}:{cfg.scenario_id}:{attempt}",
            )
            trial_id = trial["id"]

            outcome = run_trial(scenario, attempt, rng)

            if ingest is not None and cfg.publish_telemetry and station_id:
                ingest.publish(station_id, telemetry_frames(outcome, time.time_ns()))

            ev.close_trial(trial_id)
            ev.submit_verdict(
                trial_id,
                build_verdict(outcome, verifier_version=cfg.verifier_version),
                idempotency_key=f"{run_key}:{cfg.scenario_id}:{attempt}:verdict",
            )

            if not outcome.staged:
                void += 1
            elif outcome.success:
                passed += 1
            else:
                failed += 1

            # Heartbeat the lease on long runs so a slow bench never drops it.
            if lease_id and station is not None and attempt and attempt % 50 == 0:
                station.renew_lease(lease_id, ttl_seconds=cfg.ttl_seconds)

            if (attempt + 1) % 25 == 0:
                log(f"  {attempt + 1}/{cfg.budget} trials  (pass={passed} fail={failed} void={void})")
    finally:
        if lease_id and station is not None:
            station.release_lease(lease_id)
            log(f"Released lease {lease_id}.")

    log(f"Ran {cfg.budget} trials: pass={passed} fail={failed} void={void}.")
    return ev.get_run(run_id).get("run", {"id": run_id})


# ---------------------------------------------------------------------------
# Wilson interval (client-side mirror of the server's math, for reporting only -
# the server's EvaluateGate is authoritative; this lets the CLI print an interval
# even before/without a gate call).
# ---------------------------------------------------------------------------


def wilson_lower(k: int, n: int, z: float = 1.959964) -> float:
    if n == 0:
        return 0.0
    p = k / n
    denom = 1 + z * z / n
    center = p + z * z / (2 * n)
    margin = z * ((p * (1 - p) / n + z * z / (4 * n * n)) ** 0.5)
    lower = (center - margin) / denom
    return max(0.0, lower)


# ---------------------------------------------------------------------------
# CLI: drive one run against a suite that already exists (authoring is in
# so101_sim_gate.py, which composes this module for the full canonical demo).
# ---------------------------------------------------------------------------


def _print_gate(gate: dict) -> None:
    passed = gate.get("passed", False)
    inconclusive = gate.get("inconclusive", False)
    verdict = "PASS" if passed else ("INCONCLUSIVE" if inconclusive else "FAIL")
    p, f, v = int(gate.get("pass", 0)), int(gate.get("fail", 0)), int(gate.get("void", 0))
    print(f"\nGATE: {verdict}   (pass={p} fail={f} void={v}, scored={p + f})")
    for c in gate.get("checks", []):
        mark = "ok " if c.get("passed") else "XX "
        print(f"  [{mark}] {c.get('metric')}: {c.get('detail', '')}")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="Drive a Gantry eval run (sim policy) against an existing suite.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--endpoint", default="http://localhost:4780", help="Bench/Cloud base URL")
    ap.add_argument("--token", default=None, help="gtk_ access token (eval/verify scopes); localhost needs none")
    ap.add_argument("--suite", required=True, help="suite id to run")
    ap.add_argument("--scenario", default="pickplace", help="scenario id within the suite")
    ap.add_argument("--candidate", required=True, help="candidate ref, e.g. models://act-pickplace@2025.07-rc1")
    ap.add_argument("--station", default="arm=so101,sim=true", help="station tag selector")
    ap.add_argument("--budget", type=int, default=150, help="attempts to run")
    ap.add_argument("--success-rate", type=float, default=0.9, help="sim success probability")
    ap.add_argument("--stage-fail-rate", type=float, default=0.0, help="sim probability a bench is mis-staged (VOID)")
    ap.add_argument("--seed", type=int, default=None, help="RNG seed for a reproducible sim run")
    ap.add_argument("--idempotency-key", default="", help="run idempotency key (e.g. $GITHUB_RUN_ID)")
    ap.add_argument("--no-lease", action="store_true", help="skip station leasing")
    ap.add_argument("--no-telemetry", action="store_true", help="skip publishing sim telemetry")
    ap.add_argument("--gate", action="store_true", help="EvaluateGate after the run and print the result")
    ap.add_argument("--promote-on-pass", action="store_true", help="PromoteBaseline if the gate passes")
    return ap


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    transport = HttpTransport(args.endpoint, args.token)
    ev = EvalClient(transport)
    station = StationClient(transport)
    ingest = IngestClient(transport)
    rng = random.Random(args.seed)

    candidate = subject_from_ref(args.candidate)
    ev.register_subject(candidate)

    cfg = RunConfig(
        suite_id=args.suite,
        candidate=candidate,
        scenario_id=args.scenario,
        budget=args.budget,
        station_selector=args.station,
        run_idempotency_key=args.idempotency_key,
        lease=not args.no_lease,
        publish_telemetry=not args.no_telemetry,
    )
    run = run_suite(
        ev,
        station,
        ingest,
        cfg,
        sim_run_trial(args.success_rate, args.stage_fail_rate),
        rng=rng,
        log=print,
    )
    run_id = run.get("id", "")
    if args.gate and run_id:
        gate = ev.evaluate_gate(run_id)
        _print_gate(gate)
        if args.promote_on_pass and gate.get("passed"):
            b = ev.promote_baseline(run_id, idempotency_key=f"promote-{run_id}")
            print(f"Promoted candidate to baseline (rate {b.get('successRate')}).")
        return 0 if gate.get("passed") else 1
    return 0


def subject_from_ref(ref: str, kind: str = "policy") -> dict:
    """Parse `uri@version` (version optional) into a Subject. digest is required
    by RegisterSubject, so we derive a stable one from the ref."""
    uri, _, version = ref.partition("@")
    version = version or "unversioned"
    import hashlib

    digest = "sha256:" + hashlib.sha256(ref.encode()).hexdigest()[:16]
    return {"kind": kind, "uri": uri, "version": version, "digest": digest}


if __name__ == "__main__":
    sys.exit(main())
