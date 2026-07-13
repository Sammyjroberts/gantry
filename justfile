# Gantry task runner. Requires: go, cargo, pnpm, buf, docker. See docs/ARCHITECTURE.md.
set shell := ["bash", "-c"]
# Windows has multiple bash.exe (WSL in System32, Git Bash, WindowsApps stub) and
# `just` may resolve the wrong one — pin Git Bash explicitly.
set windows-shell := ["C:/Program Files/Git/bin/bash.exe", "-c"]

default:
    @just --list

# One-time repo setup: enable tracked git hooks (Conventional Commits enforcement)
setup:
    git config core.hooksPath .githooks
    @echo "Git hooks enabled (.githooks). See .claude/skills/git/SKILL.md"

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
    pnpm install --frozen-lockfile && pnpm -r build

# Build the web console and embed it into the Edge binary's UI dir.
# Guarded so a missing web build can never empty the embed dir (go:embed would break).
embed-ui: build-web
    test -f apps/web/dist/index.html
    rm -rf apps/edge/internal/ui/dist
    cp -r apps/web/dist apps/edge/internal/ui/dist

# Full release build of Edge with the real UI embedded.
# Trailing slash lets go name the binary per-OS (edge vs edge.exe).
edge-release: embed-ui
    go build -o bin/ ./apps/edge/cmd/edge

# Run all tests
test: test-go test-rust test-web

test-go:
    go test ./...

test-rust:
    cd sdk && cargo test --workspace

test-web:
    pnpm -r test

typecheck-web:
    pnpm -r exec tsc --noEmit

lint-rust:
    cd sdk && cargo clippy --workspace --all-targets -- -D warnings && cargo fmt --all --check
    cd sdk && cargo clippy -p gantry-tlm --features enabled --all-targets -- -D warnings

# Device SDK: run the tlm suite in BOTH modes (disabled is the workspace default)
test-tlm:
    cd sdk && cargo test -p gantry-wire -p gantry-tlm
    cd sdk && cargo test -p gantry-tlm --features enabled

# Prove the MCU story: no_std builds (needs `rustup target add thumbv7em-none-eabi`)
sdk-nostd:
    cd sdk && cargo build -p gantry-wire --no-default-features --target thumbv7em-none-eabi
    cd sdk && cargo build -p gantry-tlm --no-default-features --features enabled --target thumbv7em-none-eabi

# Run the Edge binary (dev)
edge:
    go run ./apps/edge/cmd/edge

# Web dev server (Vite, proxies to Edge)
web:
    cd apps/web && pnpm dev

# Run the Rust demo emitter against a local Edge
demo-emitter:
    cd sdk && cargo run -p gantry-connect --example simulator

# Local backend infra (NATS, ClickHouse, Postgres, MinIO)
compose-up:
    docker compose -f deploy/docker-compose.yml up -d

compose-down:
    docker compose -f deploy/docker-compose.yml down

bazel-build:
    bazelisk build //...

bazel-test:
    bazelisk test //...

# Windows: use @gazelle//cmd/gazelle, not //:gazelle (sh_binary runner breaks on Windows)
bazel-gazelle:
    bazelisk run @gazelle//cmd/gazelle

bazel-tidy:
    bazelisk mod tidy

compose-logs:
    docker compose -f deploy/docker-compose.yml logs -f

# Reset local backend infra INCLUDING volumes
compose-nuke:
    docker compose -f deploy/docker-compose.yml down -v
