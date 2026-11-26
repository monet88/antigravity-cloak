# Parallel `gcl2api-plus` Provider Verification (`2026-11-26`)

Verification record for [Issue #62](https://github.com/monet88/antigravity-cloak/issues/62): running an unmodified `gcl2api-plus` Docker container behind `CLIProxyAPI` + `antigravity-cloak` on an isolated `agy/gcl-*` route without disturbing built-in `agy/*` OAuth routing.

## 1. Topology & Container Configuration

| Component | Image / Version | Network | Role |
| :--- | :--- | :--- | :--- |
| `cli-proxy-api` | `eceasy/cli-proxy-api:latest` (`v8.0.72`, `e96d4c4`) + `antigravity-cloak` `v0.7.4` | `cliproxyapi_default` | Ingress gateway, `ProtectedAGY` tool/brand cloaking & OMP prompt sanitization |
| `gcl2api-plus` | `ghcr.io/wzh0718/gcl2api-plus@sha256:fd875f71d1a14e91b432c8745a85055109b9abca3539442ed94ef7257492d77f` | `cliproxyapi_default` | Upstream Antigravity Hub transport & envelope provider (`http://gcl2api-plus:7861/v1`) |

- **Spare accounts loaded:** 2 healthy Antigravity accounts (verified via `/creds/test`, 0 disabled, ~99.4%–99.7% 5h Gemini quota).
- **`gcl2api-plus` flags verified via `/config/get` and container logs:**
  - `ANTIGRAVITY_NATIVE_TRANSPORT_ENABLED=true` (`AGY Go 原生传输已启动`)
  - `SENSITIVE_WORD_REPLACE_ENABLED=false` (`sensitive_word_replace_enabled: false`)
  - `SENSITIVE_WORD_USE_DEFAULT_KEYWORDS=false` (`sensitive_word_use_default_keywords: false`)
  - `ANTIGRAVITY_SYSTEM_OBFUSCATION_MODE=off` & `ANTIGRAVITY_SYSTEM_VOWEL_OBFUSCATION_ENABLED=false`
  - `ANTIGRAVITY_SYSTEM_AS_USER_ENABLED=false`
  - `prompt_fusion_enabled: false`
  - `ANTIGRAVITY_QUOTA_GUARD_ENABLED=true`
  - `ANTIGRAVITY_SYSTEM_PROMPT_ENABLED=false` (final chosen setting; see §3)

## 2. CLIProxyAPI Routing (OMP-Only OpenAI-Compatible)

Per operator scope decision during live verification, `gcl2api-plus` is dedicated to **Oh My Pi (`oh_my_pi`, `ProtectedAGY`)** over direct OpenAI Chat Completions (`openai-compatibility` → `http://gcl2api-plus:7861/v1`), avoiding double protocol translation and avoiding Claude Code / Codex adapter overhead:

```yaml
openai-compatibility:
  - name: "gcl2api-openai"
    prefix: "agy"
    base-url: "http://gcl2api-plus:7861/v1"
    api-key-entries:
      - api-key: "<GCL2API_API_PASSWORD>"
    models:
      - name: "gemini-4-hydrogen-high"
        alias: "gcl-gemini-4-hydrogen"
        is-compat: true
      - name: "gemini-4-hydrogen-xhigh"
        alias: "gcl-gemini-4-hydrogen-xhigh"
        is-compat: true
      - name: "gemini-3.8-flash-high"
        alias: "gcl-gemini-3.8-flash"
        is-compat: true
      - name: "claude-sonnet-4-6"
        alias: "gcl-claude-sonnet-4-6"
        is-compat: true
      - name: "claude-opus-4-6-thinking"
        alias: "gcl-claude-opus-4-6"
        is-compat: true
```

**Zero-collision verification:** Running `cpa/agy/gemini-3.8-flash` (built-in OAuth) immediately after `cpa/agy/gcl-gemini-4-hydrogen` completed with exit `0` while `gcl2api-plus` container log line count stayed unchanged (`221` → `221`), confirming zero cross-routing.

## 3. `ANTIGRAVITY_SYSTEM_PROMPT_ENABLED` Decision (`false`)

Captured upstream payloads sent by `gcl2api-plus` to `daily-cloudcode-pa.googleapis.com` were inspected under both settings:

| Setting | `systemInstruction.parts` | `"You are Antigravity"` count | `"Execution environment adapter"` | Outcome |
| :--- | :--- | :---: | :---: | :--- |
| `ANTIGRAVITY_SYSTEM_PROMPT_ENABLED=true` | 2 (`parts[0]` = gcl2api AGY 2.31.2 + BRIDGE, `parts[1]` = `antigravity-cloak`) | **2 (duplicate)** | 1 | HTTP 200, but duplicates identity line |
| `ANTIGRAVITY_SYSTEM_PROMPT_ENABLED=false` | 1 (`parts[0]` = `antigravity-cloak` only) | **1** | 0 | HTTP 200, clean single identity |

**Chosen setting:** `ANTIGRAVITY_SYSTEM_PROMPT_ENABLED=false` — `antigravity-cloak` already owns the Antigravity identity line and tool/brand cloaking, so disabling `gcl2api-plus` injection eliminates the duplicate identity preamble and `Execution environment adapter` bridge.

## 4. `gcl2api-plus` Envelope & Live Client Results

### Envelope Audit (from captured `daily-cloudcode-pa.googleapis.com` payloads)
- **Native Go transport:** `AGY Go 原生传输已启动` (`agytransport`).
- **Hub UA/CL:** `antigravity_hub_version: "2.34.0"`, `antigravity_hub_cl: "1026637980"`, envelope `userAgent: "antigravity"`, `requestType: "agent"`.
- **9-key `labels` with `model_enum`:** Confirmed (`cascade_id`, `last_step_index`, `model_enum` e.g. `MODEL_PLACEHOLDER_M318` / `MODEL_PLACEHOLDER_M525`, `request_id`, `root_cascade_id`, `trajectory_id`, `used_claude`, `used_claude_conservative`, `used_non_gemini_model`).
- **Account-stable negative `sessionId`:** Confirmed (`-5645797280703417272` on `gemini-3.8-flash-high`, `-7338048392390983258` on `gemini-4-hydrogen-high`).
- **Bootstrap, 403/400/503, & Quota Guard:** `loadCodeAssist` (`tier: 'pro'`), `[403RECHECK]`, and `[QUOTA SWEEP]` active in container logs; `403` TOS accounts were correctly detected and excluded during account testing.

### Client Results
- **Oh My Pi (`omp/19.2.0`, `ProtectedAGY`) — PASS:**
  - Direct OpenAI-compat route (`cpa/agy/gcl-gemini-4-hydrogen` → `POST /v1/chat/completions` HTTP 200) and Anthropic-bridged route (`cpa/agy/gcl-gemini-3.8-flash` & `cpa/agy/gcl-gemini-4-hydrogen`) both passed multi-turn `read` (`alpha\nbeta`) + `bash` (`10639f8`) with `isError=false`, prompt cache hit (`cacheRead=11150`), `thoughtSignature` preserved on multi-turn `functionCall` parts, `<system-conventions>` → `<conventions>` + `RFC 2119` → `Normative keywords:` inbound sanitization, and assistant-text brand restoration (`Antigravity` → `omp`).
- **Claude Code (`2.1.332`) — BLOCKED / DROPPED BY OPERATOR:**
  - Verified `antigravity-cloak` cloaked inbound brand words (`Claude=0`, `Anthropic=0`, `Antigravity=13`) and `gcl2api-plus` stripped `cache_control` (`cache_control=0`, zero 400 errors), but Google Antigravity rejected requests with `429 RESOURCE_EXHAUSTED` (no quota reset time) on both `gemini-3.8-flash-high` and `gemini-4-hydrogen-high` despite ~99.7% remaining quota. Operator elected to drop Claude Code and Codex from this `gcl2api-plus` path and dedicate it to OMP.
- **Codex (`codex-cli 0.172.0`) — NOT RUN / DROPPED BY OPERATOR:**
  - Omitted per operator decision to scope `gcl2api-plus` exclusively to OMP over `openai-completions`.

## 5. Post-Run Cleanup

- `CPA_FILTER_DEBUG` removed from `cli-proxy-api` container environment and `/CLIProxyAPI/logs/cpa-filter-debug.log` truncated to `0` bytes.
- `REQUEST_CAPTURE_ENABLED=false` applied to `gcl2api-plus` and verified via `/config/get`.
- Both `cli-proxy-api` (`antigravity-cloak v0.7.4` registered & enabled, 30 models) and `gcl2api-plus` remain running and healthy.
