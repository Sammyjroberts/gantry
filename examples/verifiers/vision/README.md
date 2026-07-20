# Vision verifier (reference skeleton)

A top-down **YOLO** verifier for Gantry evals. It demonstrates the bring-your-own-compute
pattern from [RFC 0001 §6](../../../README.md): a verifier is a *client*, not a server
component — Gantry exposes the evidence and accepts a verdict, and never runs the model.

- **Where it runs:** your compute — an edge box next to the bench, a CI runner, a GPU
  host. Not Gantry.
- **What Gantry ships:** the contract + this skeleton. **Not** weights.
- **Auth:** a `verify`-scoped `TokenService` token — it can submit verdicts but can't
  drive hardware or promote a release.

## The loop

1. `ListTrials` for a run (EvalService, Connect+JSON over HTTP POST).
2. Pull each trial's top-down video chunks from the video catalog.
3. Run YOLO to classify success/fail (stubbed — plug in your weights).
4. `SubmitVerdict` with the checks, detected labels, and evidence refs.

`SubmitVerdict` upserts on `(verifier_id, verifier_version)`, so re-scoring the same
build replaces in place, and a new version re-grades stored evidence without re-running
the robot. Pin `verifierDigest` to the weights so a verdict is reproducible given the same
evidence.

## Run (illustrative)

```sh
pip install requests           # + ultralytics/opencv for real inference
export GANTRY=https://gantry.example.com
export TOKEN=...               # verify-scoped token
export RUN_ID=...              # the run to score
python verifier.py
```

This is a skeleton, not a supported binary; adapt the video route and the `score_with_yolo`
function to your deployment and model.
