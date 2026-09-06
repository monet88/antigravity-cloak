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
