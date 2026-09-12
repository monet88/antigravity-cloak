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
