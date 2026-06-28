# Postmortem: CLIProxyAPI hangs after loading antigravity-coding-filter plugin

Date: 28-06-2026
Container: `cli-proxy-api-origin` (image `eceasy/cli-proxy-api:latest`, CLIProxyAPI v7.2.42)
Plugin: `antigravity-coding-filter` (this repo)

## Symptom

- `http://127.0.0.1:8333` returned `curl: (52) Empty reply from server` (`http_code=000`) on
  every endpoint, including `/health` and `/v1/models`.
- TCP port 8333 was open on the host (docker-proxy), so `Test-NetConnection` succeeded and it
  looked like "the proxy is up" from outside.
- A misleading clue sent us down the wrong path first: a `println("init start")` inside the plugin
  `init()` did not appear in the captured `stdout` log, which suggested a crash/deadlock during
  package-level global variable initialization (`currentFilterConfig = defaultFilterConfig()`).

## Root cause

The real failure was NOT in the plugin's Go code (`defaultFilterConfig`, `rebuildCachedRegexes`,
`copyToolMappings`, `isUnambiguousToolName` are all safe; global init completed fine, proven by
`init start`/`init end` appearing on stdout after a clean boot).

The real failure: a hot-reload cycle deadlocked. CLIProxyAPI watches `config.yaml` + the auth
directory + plugin `.so` files. On any change it tears down the API server and `dlclose`/`dlopen`s
the plugins. The container log showed:

```
[14:10:23] service context cancelled, shutting down...
[14:10:23] Stopping API server...
[14:10:23] API server stopped
[14:10:23] pluginhost: plugin unloaded ... (x7)
<log stops here -- never reaches "API server started" again>
```

After that the listener on 8333 was gone. Inside the container `/proc/net/tcp` and
`/proc/net/tcp6` had no socket on port `208D` (8333). There were 3 zombie `CLIProxyAPI` processes,
every thread parked in `futex_wait_queue`, holding 0 listening sockets -> a deadlock, not a crash.

The trigger was the plugin `.so` shipped into the mount: it was 9.16 MB, built incorrectly (most
likely cross-compiled from the Windows host). A correctly built Linux `.so` for the exact same
source is 4.99 MB. A Go `c-shared` plugin carries its own embedded Go runtime; when the host
`dlclose`/`dlopen`s a badly built one during hot-reload, the plugin runtime fails to tear down
cleanly and the next reload deadlocks. The previously working `v0.0.3` build hot-reloaded fine many
times in the same log, which confirms the difference was the bad build, not the feature code.

## Why the diagnosis was initially wrong

- `println` (Go builtin) writes to stderr, not stdout. The banner `CLIProxyAPI Version: ...` is on
  stdout. Capturing only stdout (`> proxy_out.log`) hides plugin `println` output.
- Go runs variable initializers before `init()` functions, so a hang in `currentFilterConfig` would
  also prevent the `init()` `println` from firing -- which is exactly what we saw, making the
  "global init hang" theory look plausible. The debug log (filter cloaking with `changed=true`)
  disproved it: the plugin clearly ran fine once loaded.

## Fix applied

1. Rebuilt the `.so` cleanly in a Linux `golang:1.26` container, matching the CI recipe:
   `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -ldflags "-s -w" .`
   Result: 4.99 MB (vs 9.16 MB broken).
2. Replaced the `.so` in the mount (`F:\cliproxy\plugins\linux\amd64\`) and
   `docker restart cli-proxy-api-origin` to bind 8333 cleanly from a fresh boot (boot-time load
   works; only in-process hot-reload deadlocks).
3. Aligned `go.mod` SDK from `CLIProxyAPI/v7 v7.2.16` to `v7.2.42` to match the running host, ran
   `go mod tidy`, rebuilt + `go vet` clean, and redeployed the SDK-matched build.
4. Verified end to end: a Codex-disguised request to `agy/gemini-3-flash` returned HTTP 200 "PONG".

## How to avoid this next time

- ALWAYS build the `.so` for `linux/amd64` inside a Linux Go container (or CI), never cross-compile
  from Windows. Sanity check: a healthy build is ~5 MB; a ~9 MB build is a red flag.
- After swapping a `.so`, prefer a full `docker restart` over relying on hot-reload, because Go
  `c-shared` plugins are fragile under `dlclose`/`dlopen`.
- Keep `go.mod`'s `CLIProxyAPI/v7` version aligned with the host binary version (`proxy_out.log`
  banner shows it). Mismatched SDK increases reload/ABI risk.
- Debug checklist when 8333 returns empty reply:
  1. `docker exec <c> cat /proc/net/tcp /proc/net/tcp6 | grep 208D` -> is anything LISTENing?
  2. count `CLIProxyAPI` processes -> more than 1 means a stuck reload.
  3. grep `logs/main.log` for `service context cancelled` not followed by `API server started`.
  4. fix the `.so`, then `docker restart`.
- Plugin init output: plugin `println` goes to stderr. Capture with `2>&1`, or better, use the
  file-based `debugLog(...)` channel which is proven to work.

## Known pre-existing issue (out of scope, untouched)

`TestHandlePluginCallRegisterDeclaresRequestInterceptor` fails on BOTH v7.2.16 and v7.2.42: the
test looks for JSON key `stream_chunk_interceptor`, but the struct tag in `main.go` emits
`response_stream_interceptor`. This is a test/code key-name mismatch, not a regression from the SDK
bump, and runtime stream uncloaking works (debug log shows `changed=true`). Left as-is.
