#!/usr/bin/env python3
"""Reference top-down vision verifier for Gantry evals (SKELETON).

This is the BYO-compute pattern from RFC 0001 §6: a verifier is just a client
that reads a trial's evidence and POSTs a verdict back. Gantry never runs the
model — this sidecar runs on the customer's own compute (an edge box, a CI
runner, a GPU host). It:

  1. lists a run's trials over the EvalService Connect+JSON endpoint,
  2. pulls each trial's top-down video chunks from the video catalog,
  3. runs YOLO to classify success/fail (STUBBED here — plug in your weights),
  4. submits a verdict (checks + labels + evidence refs) per trial.

Gantry ships this contract + skeleton, NOT weights. Connect exposes every RPC
as a plain HTTP POST with a JSON body (proto3 JSON, camelCase), so this needs
only `requests`. Authenticate with a `verify`-scoped TokenService token so the
sidecar can submit verdicts but never drive hardware or promote a release.

Usage (illustrative):
    GANTRY=https://gantry.example.com TOKEN=... RUN_ID=... python verifier.py
"""

import os
import sys
import requests

ENDPOINT = os.environ.get("GANTRY", "http://localhost:4780").rstrip("/")
TOKEN = os.environ.get("TOKEN", "")
RUN_ID = os.environ.get("RUN_ID", "")
VERIFIER_ID = "topdown-yolo"
VERIFIER_VERSION = os.environ.get("VERIFIER_VERSION", "0.1.0")
# Pin the weights so a verdict is reproducible given the same evidence.
VERIFIER_DIGEST = os.environ.get("WEIGHTS_DIGEST", "sha256:REPLACE_ME")


def _post(rpc: str, body: dict) -> dict:
    headers = {"Content-Type": "application/json"}
    if TOKEN:
        headers["Authorization"] = f"Bearer {TOKEN}"
    r = requests.post(f"{ENDPOINT}/gantry.v1.EvalService/{rpc}", json=body, headers=headers, timeout=30)
    r.raise_for_status()
    return r.json()


def list_trials(run_id: str) -> list[dict]:
    return _post("ListTrials", {"runId": run_id}).get("trials", [])


def fetch_chunk(chunk_id: str) -> bytes:
    # The video catalog serves self-contained chunks over plain HTTP; adjust the
    # path to your deployment's video route.
    r = requests.get(f"{ENDPOINT}/video/chunks/{chunk_id}", timeout=30)
    r.raise_for_status()
    return r.content


def score_with_yolo(frames: list[bytes]) -> tuple[bool, list[str]]:
    """STUB: return (placed_ok, detected_labels).

    Replace with real inference, e.g.:
        model = YOLO("weights.pt")
        dets = model(decode(frames[-1]))
        labels = [model.names[int(c)] for c in dets[0].boxes.cls]
        return ("block" in labels and "in_slot" in labels), labels
    """
    return True, ["block", "in_slot"]


def submit_verdict(trial_id: str, placed_ok: bool, labels: list[str], chunk_ids: list[str]) -> dict:
    verdict = {
        "verifierId": VERIFIER_ID,
        "verifierVersion": VERIFIER_VERSION,
        "verifierDigest": VERIFIER_DIGEST,
        "scoredFrom": {"videoChunkIds": chunk_ids},
        "checks": [
            {
                "name": "placed",
                "phase": "PHASE_OUTCOME",
                "required": True,
                "kind": "CHECK_KIND_BOOL",
                "pass": placed_ok,
                "labels": labels,
                "evidenceRefs": chunk_ids,
            }
        ],
    }
    return _post("SubmitVerdict", {"trialId": trial_id, "verdict": verdict})


def main() -> int:
    if not RUN_ID:
        print("set RUN_ID", file=sys.stderr)
        return 2
    for trial in list_trials(RUN_ID):
        chunk_ids = trial.get("videoChunkIds", [])
        frames = [fetch_chunk(c) for c in chunk_ids]
        placed_ok, labels = score_with_yolo(frames)
        submit_verdict(trial["id"], placed_ok, labels, chunk_ids)
        print(f"scored {trial['id']}: placed={placed_ok} labels={labels}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
