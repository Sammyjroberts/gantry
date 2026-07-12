# deploy/

Local backend development infrastructure for Gantry. This is the `docker-compose`
stack that stands in for the cloud backend while you work on the bench. Everything
here is 12-factor and stateless so it maps cleanly onto managed GCP services later
(see [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md)).

> **Dev only.** All credentials in this stack are intentionally weak and hard-coded.
> Never reuse them anywhere real.

## What's in the stack

| Service | Image | Role (per ARCHITECTURE.md) |
|---|---|---|
| **nats** | `nats:2.11.4-alpine` | NATS/JetStream — the write-ahead log and the backbone between our own components (Edge internals, Edge↔Backend sync, Backend internals). Ingest writes here first; sink consumers drain to ClickHouse. |
| **clickhouse** | `clickhouse/clickhouse-server:24.8-alpine` | Backend telemetry store — high-rate multitenant time series. Database `gantry` is created on first boot. |
| **postgres** | `postgres:17.2-alpine` | Backend control plane — tenants, users, devices, dashboards, authz. |
| **minio** | `minio/minio:RELEASE.2025-04-08T15-41-24Z` | S3-compatible blob store for immutable Parquet segments (Edge→Backend sync is a file upload). GCS in cloud. |
| **minio-setup** | `minio/mc:RELEASE.2025-04-08T15-39-49Z` | One-shot job: creates the `gantry-segments` bucket, then exits. |

JetStream is file-backed (named volume) and the NATS config
([`nats/nats.conf`](nats/nats.conf)) also enables a **leafnodes** listener on
`7422`. Edge embeds its own NATS server and will connect here as a leaf node for
Edge↔Backend sync in a later milestone; the listener is wired now so that work is
unblocked.

## Ports

| Host port | Container | Service | Purpose |
|---|---|---|---|
| 4222 | 4222 | nats | Client connections |
| 8222 | 8222 | nats | HTTP monitoring (`/varz`, `/healthz`, `/jsz`) |
| 7422 | 7422 | nats | Leaf nodes (Edge connects here later) |
| 8123 | 8123 | clickhouse | HTTP interface |
| 9000 | 9000 | clickhouse | Native protocol |
| 5432 | 5432 | postgres | Postgres wire protocol |
| 9100 | 9000 | minio | S3 API — shifted off 9000 to avoid the ClickHouse native port |
| 9101 | 9001 | minio | Web console |

### Dev credentials

| Service | Value |
|---|---|
| Postgres | user `gantry` / password `gantry` / db `gantry` |
| MinIO | `minioadmin` / `minioadmin`, bucket `gantry-segments` |
| ClickHouse | user `default` (no password), db `gantry` |

## Usage

```bash
# Start everything and wait for healthchecks
docker compose -f deploy/docker-compose.yml up -d --wait
#   or: just compose-up   (no --wait)

docker compose -f deploy/docker-compose.yml ps        # status
docker compose -f deploy/docker-compose.yml logs -f   # tail logs

# Stop, keeping data volumes
docker compose -f deploy/docker-compose.yml down
#   or: just compose-down

# Nuke data too
docker compose -f deploy/docker-compose.yml down -v
```

> `up -d --wait` exits non-zero even on success because the one-shot `minio-setup`
> container exits (by design) while `--wait` is watching. If every long-running
> service reports `(healthy)` in `ps`, the stack is up. `just compose-up` avoids
> `--wait` and sidesteps this.

Quick smoke checks:

```bash
curl -s localhost:8222/healthz                       # nats  -> {"status":"ok"}
curl -s 'localhost:8123/?query=SELECT%201'           # clickhouse -> 1
pg_isready -h localhost -p 5432 -U gantry            # postgres
open http://localhost:9101                            # minio console (minioadmin/minioadmin)
```

## How this maps to GCP later

The whole point of keeping these services stateless is a clean landing on managed
GCP infrastructure — no service here holds durable state that a managed equivalent
can't own.

| Local (this stack) | Cloud (GCP) |
|---|---|
| Compute (compose services) | **GKE Autopilot** (backend services: ingestd/queryd/controld) or **Cloud Run** for stateless HTTP/Connect edges |
| Postgres | **Cloud SQL for PostgreSQL** |
| ClickHouse | **ClickHouse Cloud** (managed) |
| MinIO | **Google Cloud Storage** (Parquet segments) |
| NATS / JetStream | **NATS on GKE** (self-hosted cluster) or **Synadia Cloud** |

Because ingest only ever writes to JetStream and segments are immutable Parquet
files, the cloud story is the same shape as local: JetStream is the WAL, a sink
consumer bulk-loads ClickHouse, and Edge→Backend sync is a GCS upload followed by
`INSERT ... FORMAT Parquet`.

## Later

When cloud lands, this directory grows Terraform (infra) and Helm/Kustomize
(GKE workloads) alongside this compose file, which stays the local-dev source of truth.
