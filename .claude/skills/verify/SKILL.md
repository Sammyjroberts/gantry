# Verify Gantry (telemetry vertical slice)

Build/launch/drive recipe for verifying changes end-to-end on Windows.

## Build

```sh
just bench-release        # pnpm web build -> copied into apps/bench/internal/ui/dist -> bin/bench.exe
```

Gotchas:
- `just` on Windows needs the pinned Git Bash (`set windows-shell` in justfile) — WSL bash in System32 shadows it otherwise.
- Fresh PowerShell sessions may lack winget-installed tools; refresh with
  `$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')`.
- `go build -o bin/ ...` (trailing slash) so Windows gets `bench.exe` — an extensionless binary won't exec.
- `just bench-release` overwrites the committed placeholder `apps/bench/internal/ui/dist/index.html`; restore the placeholder before committing.

## Launch

```powershell
bin\bench.exe -data-dir .\data\bench-verify     # serves UI + ConnectRPC on :4780 (background it)
cd sdk; cargo run -p gantry-edge --example simulator   # 6 channels @ 50Hz, device sim-robot
```

Simulator prints `frames_sent/buffered/dropped/last_acked_seq` every 2s — buffered climbing with bench down, draining after connect, is the store-and-forward behavior working.

## Drive the surface

- UI served: `GET http://localhost:4780/` → 200, and fetch the `assets/*.js` referenced by index.html.
- Unary RPCs are plain JSON POSTs: `POST /gantry.v1.LiveService/ListChannels` with `{"deviceId":""}`.
- Streaming (`Subscribe`) uses Connect envelopes (`application/connect+json`, 5-byte header: 1 flag byte + u32 BE length; end frame has flag 0x02). A ~40-line Node fetch script parses it — replay burst should arrive in single-digit ms with `replaySeconds` of history, then continue live.
- Good probes: empty device_id publish → 400 invalid_argument; GET on an RPC path → 405; publish from a new device → auto-registered in ListChannels with inferred kind.
- Browser pixels need the Claude-in-Chrome extension; if disconnected, the wire-level script covers everything except rendering (rendering has vitest smoke tests).

## Teardown

`taskkill /IM bench.exe /F; taskkill /IM simulator.exe /F`, delete `data\bench-verify`, restore placeholder index.html.
