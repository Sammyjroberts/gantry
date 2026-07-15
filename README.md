# Gantry

Telemetry infrastructure for robotics and aerospace: bench-side development tooling through
multitenant cloud — one tool for design, test, and production.

- **Bench** — single-binary offline app: plug your laptop into the robot/rocket and go.
- **Edge** — OTEL-like Rust SDK + embeddable collector for devices (the customer-facing telemetry SDK).
- **Web** — browser console (live plots, 3D robot viz), served by Bench locally and by Cloud in the cloud.
- **Cloud** — multitenant cloud ingest, storage, and fleet sync.

Contracts live in `proto/gantry/v1`; the shared engine in `core/go` and `core/ts`.

## Quick start

```sh
just gen        # regen code from proto/
just build      # build Go + Rust + Web
just test       # run all tests
just bench      # run Bench at http://localhost:4780
just demo-emitter   # stream simulated robot telemetry into Bench
```

## Layout

`proto/` contracts → `core/` shared engine (Go + TS) → `apps/` thin deployable assemblies
(bench, cloud, web) → `sdk/` customer-facing Rust Edge SDK.
