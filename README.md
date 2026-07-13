# Gantry

Telemetry infrastructure for robotics and aerospace: bench-side development tooling through
multitenant cloud — one tool for design, test, and production.

- **Edge** — single-binary offline app: plug your laptop into the robot/rocket and go.
- **Connect** — OTEL-like Rust SDK + embeddable collector for devices.
- **Web** — browser console (live plots, 3D robot viz), served by Edge locally and by Backend in the cloud.
- **Backend** — multitenant cloud ingest, storage, and fleet sync.

Contracts live in `proto/gantry/v1`; the shared engine in `libs/go` and `libs/ts`.

## Quick start

```sh
just gen        # regen code from proto/
just build      # build Go + Rust + Web
just test       # run all tests
just edge       # run Edge at http://localhost:4780
just demo-emitter   # stream simulated robot telemetry into Edge
```

## Layout

`proto/` contracts → `libs/` shared engine (Go + TS) → `apps/` thin deployable assemblies
(edge, backend, web) → `sdk/` customer-facing Rust Connect SDK.
