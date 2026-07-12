# Gantry task runner. Requires: go, cargo, pnpm, buf, docker. See docs/ARCHITECTURE.md.
set shell := ["bash", "-c"]

default:
    @just --list

# Regenerate code from proto/ (Go + TS). Rust generates via build.rs in sdk/.
gen:
    buf lint
    buf generate

# Build everything with native toolchains
build: build-go build-rust build-web

build-go:
    go build ./...

build-rust:
    cd sdk && cargo build --workspace

build-web:
    cd apps/web && pnpm install --frozen-lockfile && pnpm build

# Run all tests
test: test-go test-rust test-web

test-go:
    go test ./...

test-rust:
    cd sdk && cargo test --workspace

test-web:
    cd apps/web && pnpm test

# Run the Edge binary (dev)
edge:
    go run ./apps/edge/cmd/edge

# Web dev server (Vite, proxies to Edge)
web:
    cd apps/web && pnpm dev

# Run the Rust demo emitter against a local Edge
demo-emitter:
    cd sdk && cargo run --example simulator

# Local backend infra (NATS, ClickHouse, Postgres, MinIO)
compose-up:
    docker compose -f deploy/docker-compose.yml up -d

compose-down:
    docker compose -f deploy/docker-compose.yml down
