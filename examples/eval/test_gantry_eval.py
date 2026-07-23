"""Pure-part tests for the eval sim runner + Connect client. No hardware, no
network: the client runs against a RecordingTransport that captures the exact
JSON bodies, so we assert request *shaping* against the proto contract.

Run:  python -m pytest examples/eval -q
"""

import json
import random

import pytest

import gantry_eval as ge
import so101_sim_gate as gate


# ---------------------------------------------------------------------------
# Outcome sampling converges to the target rate (the sim's core promise).
# ---------------------------------------------------------------------------


def test_sim_success_rate_converges():
    rng = random.Random(42)
    run = ge.sim_run_trial(0.9)
    n = 8000
    hits = sum(run({"id": "s"}, i, rng).success for i in range(n))
    assert abs(hits / n - 0.9) < 0.02  # within 2 points of the target


def test_sim_zero_and_one_rates_are_extreme():
    rng = random.Random(1)
    always = ge.sim_run_trial(1.0)
    never = ge.sim_run_trial(0.0)
    assert all(always({"id": "s"}, i, rng).success for i in range(200))
    assert not any(never({"id": "s"}, i, rng).success for i in range(200))


def test_stage_fail_rate_produces_voids():
    rng = random.Random(7)
    run = ge.sim_run_trial(0.9, stage_fail_rate=0.2)
    outcomes = [run({"id": "s"}, i, rng) for i in range(4000)]
    voids = sum(not o.staged for o in outcomes)
    assert abs(voids / len(outcomes) - 0.2) < 0.03
    # A mis-staged attempt is never counted as a success.
    assert all(o.staged for o in outcomes if o.success)


def test_seeded_run_is_reproducible():
    a = [ge.sim_run_trial(0.85)({"id": "s"}, i, random.Random(99)).success for i in range(50)]
    b = [ge.sim_run_trial(0.85)({"id": "s"}, i, random.Random(99)).success for i in range(50)]
    assert a == b


# ---------------------------------------------------------------------------
# Idempotency-key stability: same (run, scenario, attempt) -> same key; any
# component change -> a different key. This is what makes a retried CI step
# re-attach instead of double-running.
# ---------------------------------------------------------------------------


def _keys_from_run(run_key="run-1", scenario="sc", attempts=3):
    responses = {
        "/gantry.v1.EvalService/StartRun": {"run": {"id": "run-x"}},
        "/gantry.v1.EvalService/OpenTrial": lambda b: {"trial": {"id": "t-" + str(b["attempt"])}},
        "/gantry.v1.EvalService/GetRun": {"run": {"id": "run-x"}},
    }
    ev = ge.EvalClient(ge.RecordingTransport(responses))
    cfg = ge.RunConfig(
        suite_id="su", candidate={"digest": "d"}, scenario_id=scenario,
        budget=attempts, lease=False, publish_telemetry=False, run_idempotency_key=run_key,
    )
    # A run_trial that always succeeds; capture the OpenTrial/SubmitVerdict keys.
    ge.run_suite(ev, None, None, cfg, ge.sim_run_trial(1.0), rng=random.Random(0))
    open_keys, verdict_keys = [], []
    for path, body in ev.t.calls:
        if path.endswith("/OpenTrial"):
            open_keys.append(body["idempotencyKey"])
        elif path.endswith("/SubmitVerdict"):
            verdict_keys.append(body["idempotencyKey"])
    return open_keys, verdict_keys


def test_open_trial_keys_are_stable_and_unique():
    open_keys, verdict_keys = _keys_from_run()
    assert open_keys == ["run-1:sc:0", "run-1:sc:1", "run-1:sc:2"]
    assert verdict_keys == ["run-1:sc:0:verdict", "run-1:sc:1:verdict", "run-1:sc:2:verdict"]
    assert len(set(open_keys)) == len(open_keys)  # unique per attempt


def test_replay_same_run_key_yields_same_trial_keys():
    a, _ = _keys_from_run(run_key="ci-42")
    b, _ = _keys_from_run(run_key="ci-42")
    assert a == b  # a retried job produces byte-identical idempotency keys


def test_different_run_key_changes_keys():
    a, _ = _keys_from_run(run_key="ci-1")
    b, _ = _keys_from_run(run_key="ci-2")
    assert a != b


# ---------------------------------------------------------------------------
# Request shaping against the proto contract (camelCase, u64-as-string, enums).
# ---------------------------------------------------------------------------


def _record(responses=None):
    t = ge.RecordingTransport(responses or {})
    return t, ge.EvalClient(t)


def test_start_run_body_shape():
    t, ev = _record({"/gantry.v1.EvalService/StartRun": {"run": {"id": "r1"}}})
    ev.start_run("su1", {"kind": "policy", "digest": "d"}, target_selector="arm=so101", replicas=2, idempotency_key="k")
    path, body = t.calls[-1]
    assert path == "/gantry.v1.EvalService/StartRun"
    assert body["suiteId"] == "su1"
    assert body["targetSelector"] == "arm=so101"
    assert body["replicas"] == 2  # uint32 -> plain number
    assert body["idempotencyKey"] == "k"
    assert body["candidate"]["kind"] == "policy"


def test_open_trial_u64_fields_are_strings():
    t, ev = _record({"/gantry.v1.EvalService/OpenTrial": {"trial": {"id": "t1"}}})
    ev.open_trial("r1", "sc", 5, station_id="st", seed=2**40, idempotency_key="k")
    _, body = t.calls[-1]
    assert body["attempt"] == 5  # uint32 -> number
    assert body["seed"] == str(2**40)  # uint64 -> string
    assert isinstance(body["seed"], str)


def test_submit_verdict_enum_and_value_shape():
    outcome = ge.TrialOutcome(success=True, task_time_s=21.5, staged=True)
    verdict = ge.build_verdict(outcome, verifier_version="1.2.3")
    names = {c["name"]: c for c in verdict["checks"]}
    assert names["space_ready"]["phase"] == "PHASE_PRECONDITION"
    assert names["space_ready"]["kind"] == "CHECK_KIND_BOOL"
    assert names["placed"]["phase"] == "PHASE_OUTCOME"
    assert names["placed"]["pass"] is True
    assert names["task_time_s"]["kind"] == "CHECK_KIND_NUMERIC"
    assert names["task_time_s"]["value"] == 21.5
    assert names["task_time_s"]["op"] == "<="
    assert verdict["verifierVersion"] == "1.2.3"
    assert isinstance(verdict["scoredNs"], str)  # fixed64 -> string


def test_failed_outcome_marks_placed_false():
    v = ge.build_verdict(ge.TrialOutcome(success=False, task_time_s=30.0))
    placed = next(c for c in v["checks"] if c["name"] == "placed")
    assert placed["pass"] is False


def test_telemetry_frames_land_under_joints_and_task():
    o = ge.TrialOutcome(success=True, task_time_s=12.0, telemetry={j: 1.0 for j in ge.JOINTS})
    frames = ge.telemetry_frames(o, t_ns=123)
    packets = {f["packet"] for f in frames}
    assert set(ge.JOINTS) <= packets
    assert "task" in packets
    for f in frames:
        assert isinstance(f["timestampNs"], str)  # fixed64 -> string
        assert "f64" in f["value"]  # Value oneof


def test_publish_batch_sequence_increments_and_is_string():
    t = ge.RecordingTransport()
    ing = ge.IngestClient(t)
    ing.publish("dev", [])
    ing.publish("dev", [])
    seqs = [body["batch"]["sequence"] for _, body in t.calls]
    assert seqs == ["1", "2"]  # uint64 -> string, monotonic


def test_subject_from_ref_parses_uri_and_version():
    s = ge.subject_from_ref("models://act-pickplace@2025.07-rc1")
    assert s["uri"] == "models://act-pickplace"
    assert s["version"] == "2025.07-rc1"
    assert s["digest"].startswith("sha256:")
    # Same ref -> same digest (content-addressed, idempotent RegisterSubject).
    assert ge.subject_from_ref("models://act-pickplace@2025.07-rc1")["digest"] == s["digest"]


def test_subject_from_ref_defaults_version():
    assert ge.subject_from_ref("firmware://x", kind="firmware")["version"] == "unversioned"


# ---------------------------------------------------------------------------
# Wilson lower bound (client mirror of the server math).
# ---------------------------------------------------------------------------


def test_wilson_lower_matches_known_value():
    # 138/150 = 0.92; Wilson 95% lower bound ~ 0.866.
    lb = ge.wilson_lower(138, 150)
    assert 0.86 < lb < 0.875


def test_wilson_zero_trials_is_zero():
    assert ge.wilson_lower(0, 0) == 0.0


# ---------------------------------------------------------------------------
# End-to-end loop shaping against a scripted transport (no server): the runner
# opens/closes/scores every attempt and leases+releases exactly once.
# ---------------------------------------------------------------------------


class _ScriptedTransport(ge.Transport):
    """Answers the handful of methods run_suite calls, tracking counts."""

    def __init__(self):
        self.counts = {}
        self.trial_n = 0

    def post(self, path, body):
        method = path.rsplit("/", 1)[-1]
        self.counts[method] = self.counts.get(method, 0) + 1
        if method == "CheckTarget":
            return {"ok": True, "detail": "1 match, 1 online, 1 free (need 1)"}
        if method == "Lease":
            return {"leases": [{"id": "lease-1", "stationId": "st-1"}], "stations": []}
        if method == "ListStations":
            return {"stations": [{"id": "st-1"}]}
        if method == "StartRun":
            return {"run": {"id": "run-1"}}
        if method == "OpenTrial":
            self.trial_n += 1
            return {"trial": {"id": f"trial-{self.trial_n}"}}
        if method in ("CloseTrial", "SubmitVerdict"):
            return {"trial": {"id": "trial"}}
        if method == "GetRun":
            return {"run": {"id": "run-1"}}
        return {}


def test_run_suite_drives_full_loop_and_leases_once():
    t = _ScriptedTransport()
    ev, st, ing = ge.EvalClient(t), ge.StationClient(t), ge.IngestClient(t)
    cfg = ge.RunConfig(
        suite_id="su", candidate={"digest": "d"}, scenario_id="sc", budget=10,
        run_idempotency_key="k",
    )
    ge.run_suite(ev, st, ing, cfg, ge.sim_run_trial(0.8), rng=random.Random(3))
    assert t.counts["OpenTrial"] == 10
    assert t.counts["CloseTrial"] == 10
    assert t.counts["SubmitVerdict"] == 10
    assert t.counts["Lease"] == 1
    assert t.counts["ReleaseLease"] == 1  # released even though we ran to completion
    assert t.counts["RegisterChannels"] == 1  # channels registered once
    assert t.counts["PublishBatch"] == 10  # telemetry per attempt


def test_run_suite_without_station_skips_leasing():
    t = _ScriptedTransport()
    ev = ge.EvalClient(t)
    cfg = ge.RunConfig(
        suite_id="su", candidate={"digest": "d"}, scenario_id="sc", budget=3,
        lease=False, publish_telemetry=False,
    )
    ge.run_suite(ev, None, None, cfg, ge.sim_run_trial(1.0), rng=random.Random(0))
    assert "Lease" not in t.counts
    assert t.counts["OpenTrial"] == 3


# ---------------------------------------------------------------------------
# Suite authoring payload (so101_sim_gate) matches the RFC's canonical spec.
# ---------------------------------------------------------------------------


def test_ensure_suite_payload_matches_rfc_spec():
    t = ge.RecordingTransport({
        "/gantry.v1.EvalService/ListSuites": {"suites": []},
        "/gantry.v1.EvalService/UpsertSuite": lambda b: {"suite": {**b["suite"], "id": "su-x"}},
    })
    ev = ge.EvalClient(t)
    suite = gate.ensure_suite(ev)
    assert suite["id"] == "su-x"
    _, body = t.calls[-1]
    s = body["suite"]
    assert s["name"] == "arm-pickplace-sim"
    sc = s["scenarios"][0]
    assert sc["trialBudget"] == 150
    assert sc["minScored"] == 100
    g = json.loads(s["gateJson"])[0]
    assert g["metric"] == "success_rate"
    assert g["op"] == "non_inferior"
    assert g["margin"] == 0.03
