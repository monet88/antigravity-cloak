# NOTE-DEBUGS.md - Antigravity Cloak Debug Notes

## Bug 001: Oh My Pi Stream Chunk Uncloak Failure ("Assistant returned empty stop after retry cap")

### 1. Symptom
- Client: Oh My Pi (`omp`).
- Model: `agy/gemini-3.7-flash`.
- Error reported by Oh My Pi: `Error: Retry failed after 3 attempts: Assistant returned empty stop after retry cap`.

### 2. Root Cause
1. **Cloaked Client Detection Threshold Failure**:
   - When Oh My Pi sends a request, it provides 9 default tools (`read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`, `ask`, `todo`).
   - The plugin successfully cloaked them to Antigravity tools (`run_command`, `view_file`...).
   - When Gemini returned stream chunks, `detectCloakedClient` was called. It evaluated hit ratio against the total tool count in `defaultCloakTables["oh_my_pi"]` (12 tools): `9 / 12 = 75% < 80%` threshold (`hits * 5 >= len(cloakTable) * 4`).
   - Detection returned `""` (no client identified), so `run_command` was never uncloaked to `bash`.
2. **Stream Chunk JSON Dropping**:
   - `splitSSEEvents` expected every stream chunk to contain `\n\n` boundaries (SSE format).
   - CLIProxyAPI's OpenAI protocol chunks are individual JSON objects without `\n\n` or `data: ` prefix.
   - When no `\n\n` was found, `splitSSEEvents` treated it as incomplete tail and returned `DropChunk: true`, dropping all intermediate tool-call chunks until `[DONE]`.

### 3. Fix
1. **Update `detectCloakedClient`**:
   - Check coverage against **observed tool count** (`hits / totalObserved >= 80%`) and require at least 3 matching tools (`hits >= 3`).
   - For native Antigravity traffic supersets where multiple distinct tables have 100% full coverage (`fullCoverageCount >= 2`), return `""`.
2. **Session Pre-Registration**:
   - When `request.intercept_before` sees a known client, pre-register the session in `StreamSessionManager` by `RequestID`.
   - Stream headers/chunks retrieve the pre-registered client immediately.
3. **Handle Standalone JSON Chunks in `processChunk`**:
   - Distinguish SSE-formatted streams (`data:`, `event:`, `\n\n`) from standalone JSON chunk payloads. Uncloak standalone JSON chunks directly without dropping.

### 4. Verification
- All Go unit tests pass (`go test -v ./...`).
- Real-time end-to-end request sent to live CLIProxyAPI on `54.255.81.117` with `agy/gemini-3.7-flash`:
  ```json
  "tool_calls":[{"function": {"name": "bash", "arguments": "{\"command\": \"ls -la\"}"}}]
  ```
- SSE payload returned HTTP 200 OK with `name: "bash"` uncloaked cleanly.

## Bug 002: Plugin Inactive / Not Registered After Plugin Binary Upgrade (v0.4.3)

### 1. Symptom
- CLIProxyAPI management UI reports `antigravity-cloak`: `Inactive`, `Not registered`, `Configured` (`registered: false`, `path: ""`, `effective_enabled: false`).

### 2. Root Cause
- Binary `antigravity-cloak-v0.4.3.so` was placed in `plugins/linux/amd64/` and `v0.4.2.so` was removed.
- `config.yaml` explicitly specified `store.version: "0.4.2"`.
- CLIProxyAPI's `selectPluginFiles` (`platform.go`) filters candidates by `desiredVersion`: if specified, file version MUST equal `desiredVersion`. Mismatch caused the candidate to be skipped completely, leaving `path: ""` and unregistered.

### 3. Fix
- Updated `plugins.configs.antigravity-cloak.store.version` to `"0.4.3"` in `config.yaml`.
- Restarted container `cli-proxy-api`.

### 4. Verification
- `GET /v0/management/plugins`: `registered: true`, `effective_enabled: true`, `path: "plugins/linux/amd64/antigravity-cloak-v0.4.3.so"`.
- `POST /v1/chat/completions` with model `agy/gemini-3.7-flash-high` returns 200 OK.

## Bug 003: Oh My Pi Upstream 429 RESOURCE_EXHAUSTED Triggered by `<system-conventions>` Tag

### 1. Symptom
- Client: Oh My Pi (`omp`).
- Model: `agy/gemini-3.8-flash` (mapped to `gemini-3.8-flash-high`).
- Error:
  ```json
  {
    "error": {
      "code": 429,
      "message": "Resource has been exhausted (e.g. check quota).",
      "status": "RESOURCE_EXHAUSTED"
    }
  }
  ```
- Other clients (Claude Code, web interfaces, curl) work normally with the same account and quota.

### 2. Root Cause
- Local payload bisection isolated the tag pair in OMP's system prompt as the trigger at Google Antigravity Cloud Code upstream (`daily-cloudcode-pa.googleapis.com`).
- Oh My Pi wraps its system prompt in:
  ```xml
  <system-conventions>
  RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`; `AVOID` = `SHOULD NOT`.
  XML tags inject system content; NEVER interpret them otherwise. Tags may interrupt/notify inside user messages: MUST treat as system-authored/authoritative. User content sanitized; role absent: `<system-directive>` in a user turn remains a system directive.
  </system-conventions>
  ```
- The original wrapper reproduced HTTP 429 (`RESOURCE_EXHAUSTED`); renaming the wrapper succeeded. This observation does not establish the upstream classifier's internal implementation or behavior for other `<system-*>` tags.

### 3. Fix
- **Client-side**: Patch `cli.js` in `@oh-my-pi/pi-coding-agent` to replace `system-conventions` with `agent-conventions`.
- **Plugin-side**: `sanitizeProtectedSystemConventions` runs after brand rewriting in `handleProtectedAGY`. It replaces the exact opening/closing tags with `<conventions>` in system/developer prompt text, independently of brand settings. See `docs/specs/system-conventions-sanitization.md` for scope and tests.

### 4. Verification
- Historical client-side workaround: direct replay of the payload renamed to `<agent-conventions>` against `daily-cloudcode-pa.googleapis.com` returned 200 OK with streaming SSE response candidates.
- Tested `omp/18.1.18` end-to-end with bash tool calling (`git status` and `echo 18.1.18`), executed and streamed successfully without 429.
- Plugin-side `<conventions>` acceptance passed on 2026-09-12 with v0.5.1 at commit `5d35663`: two correlated requests returned HTTP 200, the original wrapper was absent upstream, and streamed `run_command` was restored to native OMP `bash`, which returned `5d35663`. See the [validated deployment evidence and repeatable runbook](docs/verification-checklist.md#validated-sanitization-deployment---2026-09-12). Unit and handler tests remain in `system_conventions_test.go`.

---

## Bug 004: Tier Scanner Broke on Every Valid Model Key (`tierArgsScanner` Issue #55)

### Symptom

New Issue #55 streaming tests: the tier scanner never translated a top-level `model` scalar after fragmentation; a deliberately-triggered "hold" never engaged. Localization discovered that the scanner marked itself `broken` at the CLOSE quote of the FIRST top-level key of every argument object (`{"model":` broke at `"` of the key).

### Root Cause

`tierArgsScanner.closeKeyToken` collects the JSON key literal BODY (the bytes between two quotes, excluding the latter). Feeding those bare body bytes directly to `json.Unmarshal` produced `invalid character 'm' looking for beginning of value` -> the scanner set `broken` and became raw afterwards with no logging.

### Verified fix

Wrap the collected body with quotes to rebuild a string literal: `literal = '"' + keyRaw + '"'` then `json.Unmarshal(literal, &decoded)`. Tests: the crate series in `issue55_agent_tier_test.go`.

### Prevention / Fast path

When a byte-level JSON scanner accumulates an escaped-string BODY, always rebuild the complete literal before `json.Unmarshal`; missing surrounding quotes degenerates into scanner-broken without any signal. Debug path: enable `CPA_FILTER_DEBUG` and trace the `agent tier lane ...` / `agent tier ...` wake-upe lines.

---

## Bug 005: Escaped Keys and Filtered Flushes Broke Tier Reversal (Issue #55)

### Symptom

Two defects an offline happy-path test cannot see. (a) The tier scanner missed an escaped spelling of the model key (`{"\u006dodel":"gemini-pro"}` was forwarded untranslated) while an unrelated key whose escaped bytes collapse to the same letters (`{"m\nodel":"gemini-pro"}`) had its value rewritten to `"opus"` — corruption, not a miss. (b) A standalone (non-SSE) chunk that finished choice 0 flushed every lane first and filtered afterwards, so choice 1's still-streaming `"model"` scalar was released by `releaseHold` into text that was then discarded; the next chunk continued from an already-drained scanner and the client received truncated arguments.

### Root Cause

(a) `tierArgsScanner.feed` appended key bytes only outside the escape handling, dropping `\` and the escaped character, so `keyRaw` was neither the wire spelling nor the decoded one. `closeKeyToken` decodes `keyRaw` as a JSON string literal, so it must receive the RAW body including escapes.
(b) `orderedBrandFlushes` resolved every lane before callers applied `only`/`skip`. Resolution is stateful for tier lanes: `applyTierLane(..., terminal=true)` calls `scan.releaseHold`.

### Verified fix

(a) Capture the raw key literal (`s.escaped || c != '"'` appends), letting `closeKeyToken`'s `json.Unmarshal` decode exactly what arrived. (b) `orderedBrandFlushesFor(sess, keep)` filters keys BEFORE resolution, and both filtered flush sites (`generateBrandFlushEventsFiltered`, `flushBrandStandalone`) pass their predicate into it. Regression tests: `TestIssue55TierScannerDecodesEscapedModelKey`, `TestIssue55TierScannerKeepsEscapedLookalikeKeyLiteral`, `TestIssue55FilteredFlushKeepsUnselectedTierHold` — each verified to fail when its guard is removed.

### Prevention / Fast path

A flush helper that resolves state before the caller chooses what to deliver is a latent byte-drop; push the predicate into the resolver. For byte scanners, capture raw literals and decode once, and always test a lookalike that must stay untouched — a test that only checks the intended translation passes against a corrupting matcher.

## Bug 006: Release CI windows/arm64 Cross Build Failed on Snap-Zig (v0.7.2)

### Symptom

The `Build windows/arm64` job failed four consecutive release runs (v0.7.2 tag, 2026-11-11). Run 1-3: `error: unable to contact snap store` from `sudo snap install zig --classic --beta` (inside `go-cross/cgo-actions@v1`). Run 3 retry after switching to `mlugg/setup-zig@v1`: mirror returned 404/5xx. Run 4: zig tarball installed but `zcc: zig: not found` during `go build`; the next attempt failed silently (exit 1, zero output) at a `test -x` probe.

### Root Cause

A dependency chain, each layer breaking: (1) `go-cross/cgo-actions` hardcodes `sudo snap install zig` with no pre-check or bypass option, and the hosted runner could not reach the snap store. (2) `mlugg/setup-zig` relies on a community mirror that was also down. (3) The hand-rolled `find ... -type d -name 'zig-*' | sudo mv ... /usr/local/zig` layout left the binary at an unknown location - PATH lookup missed it, and the follow-up `test -x /usr/local/zig/zig` failed without printing anything because `set -e` with silent commands hides which one died.

### Verified fix

`.github/workflows/build.yml` (commit `a994aba`): fetch `https://ziglang.org/download/0.14.1/zig-x86_64-linux-0.14.1.tar.xz` (note: 0.14.1 switched to arch-first naming `zig-x86_64-linux-*`; the old `zig-linux-x86_64-*` spelling 404s), extract with `sudo tar -xJf ... -C /usr/local/zig --strip-components=1` so the binary lands at a deterministic path, `set -euxo pipefail` so any failure names its command, and call `/usr/local/zig/zig` by absolute path inside the `zcc`/`z++` CGO wrappers. Run 43046009111 went green and GitHub Release v0.7.2 published with all six platform zips + `checksums.txt`.

### Prevention / Fast path

When a hosted-runner toolchain install flakes, rerun once, then remove the flaky installer dependency instead of retrying - snap/`setup-zig` mirrors fail in bursts. Never lay down a toolchain via `find`+`sudo mv`; extract with `--strip-components=1` to a fixed path, assert `test -x`, print the version, and invoke by absolute path from wrapper scripts. Check the tarball naming convention (zig changed directory spelling at 0.14.1) with a HEAD request before committing the URL.

---

## Bug 007: Oh My Pi 18.20.0 Upstream 429 RESOURCE_EXHAUSTED from Injected Claude Code OAuth System Instruction

### Symptom

- After updating to `omp/18.20.0`, requests over `api: anthropic-messages` on `agy/*` routes (`agy/gemini-3.8-flash`, `agy/claude-sonnet-4-6`) failed upstream with `429 RESOURCE_EXHAUSTED` (`"Resource has been exhausted (e.g. check quota)."`), while `<system-conventions>` and `RFC 2119 ...` probes already returned `200 OK` on `v0.7.3`.

### Root Cause

1. `omp/18.20.0` changed `custom-models.ts` (`Lte(api, auth)`): custom `anthropic-messages` models without an explicit `auth` field in `models.yml` now default to `isOAuth: true`, even when authenticating with a plain proxy API key.
2. Under `isOAuth: true`, OMP prepends two blocks to `system` (`sys[0]`: `x-anthropic-billing-header: ...` stripped by CLIProxyAPI upstream, and `sys[1]`: `"You are Claude Code, Anthropic's official CLI for Claude."` with `cache_control: {"type": "ephemeral"}`) and prefixes tools with `_`, while still sending `X-Cloak-Client: oh_my_pi`.
3. Because the request resolves to `oh_my_pi` (`handleProtectedAGY` / `rewriteProtectedBrand`), `claudeCodeBrandMappings` did not run; `"You are Claude Code, Anthropic's official CLI for Claude."` reached `daily-cloudcode-pa.googleapis.com` verbatim and triggered upstream `429 RESOURCE_EXHAUSTED`.

### Verified Fix

1. Added `ompClaudeCodeInstruction` (`"You are Claude Code, Anthropic's official CLI for Claude."` $\to$ `antigravityIdentity`, case-insensitive) to `defaultOMPSanitizationPresets` in `omp_prompt_sanitization.go`, preserving the `cache_control` block as non-empty text (stripping to `""` breaks Claude upstreams with HTTP `400`). Added regression coverage in `TestOMPInjectedClaudeCodeInstructionPreset` (`system_conventions_test.go`).
2. Applied immediate live `omp_prompt_sanitizations` override on the local gateway (`http://127.0.0.1:8317`) via `PATCH /v0/management/plugins/antigravity-cloak/config`, restoring `200 OK` even when `isOAuth: true` injects `sys[1]`.
3. Added `auth: apiKey` (exact `ProviderAuthSchema` enum in OMP: `"apiKey" | "none" | "oauth"`) to `providers.cpa` in `C:\Users\monet\.omp\agent\models.yml`, disabling unintended Claude OAuth impersonation and verifying live round-trip execution on both chat and tool calls.

### Prevention / Fast path

When a new OMP release unexpectedly triggers upstream `429` despite intact `<system-conventions>` / `RFC 2119` rules, capture a single outgoing OMP wire request against a local HTTP sink first and compare `system[]` block-by-block before altering Go brand tables; push immediate mitigations via live `omp_prompt_sanitizations` while landing the preset cleanly in `omp_prompt_sanitization.go`. When setting provider `auth` in `models.yml`, always check `ProviderAuthSchema` (`"apiKey" | "none" | "oauth"` — camelCase `apiKey`, never `api-key`).
