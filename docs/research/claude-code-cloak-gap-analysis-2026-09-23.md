# Claude Code Cloak — Gap Analysis Against Oh My Pi

**Date:** 2026-09-23
**Revision audited:** `ff3479e` (`main.go`, `pluginVersion = "0.5.2"`)
**Scope:** Read-only audit of the Claude Code (`claude_code`) cloak path — tool table,
client identity resolution, `X-Cloak-Client` handling, reverse/uncloak, and brand
restoration — compared against the Oh My Pi (`oh_my_pi`) and Codex (`codex`) paths as
currently implemented. No code was changed by this pass.

Client-specific table ownership note: the AGY native surface itself (the 20 tools) is
defined in [the Antigravity tool surface reference](antigravity-tool-surface-2026-09-06.md);
the Codex surface and its per-mode carriers are in
[the Codex surface reference](codex-tool-surface-2026-09-12.md).

> [!IMPORTANT]
> **SUPERSEDED STATUS (2026-09-25 / Issues #32, #36):**
> This audit records the historical state of the Claude Code cloak at revision `ff3479e` (v0.5.2).
> As of v0.6.0 (Issues #32, #36):
> - `Glob` maps to `find_by_name` (not `list_dir`).
> - Static source identity inventory (`claudeCodeSourceIdentityInventory`) and distinctive tool guards are implemented.
> - Tier-2 tools (including `ToolSearch`, `Skill`, `Workflow`, subagent control, MCP resources, and former Group C tools such as `NotebookEdit`, `ReportFindings`, `EnterPlanMode`, `ExitPlanMode`, `EnterWorktree`, `ExitWorktree`, `DeferredToolPlaceholder`) are cloaked via stable shared aliases (`wp_*`) rather than pass-through or speculative AGY mappings.
> - Request-scoped alias plans (`requestsRequestScopedReverse`) and fail-closed 503 (`tool_cloak_required`) on missing/duplicate RequestID or collision are active for Claude Code.
>
> Refer to [CONTEXT.md](../../CONTEXT.md) and [AGENTS.md](../../AGENTS.md) for authoritative current behavior.

---

## 1. Status Summary

`claude_code` is a second-class client. It has a cloak table and body-based detection,
and nothing else. Every mechanism that was built for OMP, or built for Codex during the
2026-09-12 pass, is absent for Claude Code.

| Property | `oh_my_pi` | `codex` | `claude_code` |
| :--- | :--- | :--- | :--- |
| Cloak table | 9 canonical, fail-closed | 6 (code mode) | 11, never audited |
| `X-Cloak-Client` privilege | ProtectedAGY / exact 503 | identity only | identity only |
| Static source inventory | `ompSourceIdentityInventory` (`main.go:3213`) | `codexSourceIdentityInventory` (`main.go:3258`) | **none** |
| Distinctive-tool guard | `clientDistinctiveTools["oh_my_pi"]` (`main.go:3326`) | none needed (inventory) | **none** |
| UA evidence | `omp/` (`main.go:122`) | — | **explicitly refused** (`plugin_ua_evidence_test.go:198`) |
| Request-scoped reverse | via ProtectedAGY | `requestsRequestScopedReverse` (`main.go:1200`) | **no** |
| Brand restoration | `Antigravity -> omp` | — | **one-directional only** |
| Live end-to-end verification | 9 tools verified | 6 tools verified | `tests/test_client.py` smoke only |

The CC table as deployed (`main.go:3138-3143`):

```go
"claude_code": {
    "Bash": "run_command", "Edit": "replace_file_content", "Read": "view_file",
    "Write": "write_to_file", "Grep": "grep_search", "Glob": "list_dir",
    "Agent": "invoke_subagent", "AskUserQuestion": "ask_question",
    "ToolSearch": "search_web", "Skill": "call_mcp_tool", "Workflow": "schedule",
},
```

---

## 2. `X-Cloak-Client` Mechanism — How Far It Gets for Claude Code

The header is parsed by one function for all clients (`parseExplicitClientMarker`,
`main.go:3628`), but only `oh_my_pi` is given a special branch:

- `main.go:3671` — a single resolved client sets `res.isOMP = true; res.valid = true` only
  when the value is `oh_my_pi`. Every other client takes the generic path, where `valid`
  merely means "the active `ToolMappings` entry is non-empty".
- `main.go:957` — the `ProtectedAGY` admission branch is keyed on `marker.isOMP && isAGY`.
  Consequently `X-Cloak-Client: claude_code` on an `agy/*` route gets **no** admission
  gate: no strict single-document JSON decode, no `inspectAndValidateProtectedTools`
  collision check, no `canonicalOMPSafeMappingSet` validation, no request-scoped
  `activeReverse` derivation, and no exact 503 rejection on failure.
- `main.go:1200` — `requestsRequestScopedReverse` returns `client == "codex"`. The CC
  reverse table is therefore **never narrowed** to the names a request actually declared.
  Every AGY target the CC table owns is live in the reverse for every CC request, whether
  or not CC declared the corresponding source.
- `main.go:1054-1056` — `brandRestorationEnabled` is only ever set on the
  `routeKindProtectedAGY` route (`main.go:934`). The generic response path calls
  `reverseBrandInResponseBody` (`main.go:1122`) only on the `oh_my_pi` fall-through, so
  CC assistant text keeps the literal `Antigravity`.

**Conclusion:** the "identify the client by an explicit marker and enforce it" mechanism
that OMP has, and that Codex adopted as an identity-only variant
(ADR 0004 §5), does not exist for Claude Code in any form beyond "the header picks which
table is used".

---

## 3. Missing Tool Coverage vs. the AGY Native Surface

### Group A — present but semantically wrong (5 entries, real risk)

| CC tool | Mapped to | Why it is wrong | Correct target |
| :--- | :--- | :--- | :--- |
| `Glob` | `list_dir` | AGY `list_dir` lists the immediate children of one directory and takes no pattern. `Glob` is a pattern matcher. The upstream call cannot express the request. | `find_by_name` (what OMP's `glob` already uses) |
| `ToolSearch` | `search_web` | `ToolSearch` discovers deferred tools; AGY `search_web` queries the internet. AGY answers a web search for a tool-discovery call. | likely pass-through |
| `Skill` | `call_mcp_tool` | Schema mismatch: AGY `call_mcp_tool` requires `ServerName` + `ToolName` + `Arguments`; `Skill` sends `skill` + `args`. | likely pass-through |
| `Workflow` | `schedule` | Schema mismatch: AGY `schedule` takes `Prompt` / `DurationSeconds` / `CronExpression`; `Workflow` sends a `script` body. | `ScheduleWakeup`, or pass-through |
| `Agent` | `invoke_subagent` | Semantically fine, but CC's subagent control surface (`ListAgents`, `SendMessage`) is left unmapped while the launcher is mapped. | keep, and see Group B |

`Glob -> list_dir` is not a cosmetic issue: it is a live functional break. `Glob` is one
of the most-used CC tools, and the cloak silently degrades it to a flat directory listing
on every cloaked request. Note that `filter_test.go:1329` asserts `list_dir` must **not**
appear as a cloaked OMP target — the OMP path is correct, and the CC table diverges from
it without a recorded reason.

### Group B — AGY target exists, CC tool exists, no mapping (7 candidates)

| AGY native tool | CC tool | Notes |
| :--- | :--- | :--- |
| `read_url_content` | `WebFetch` | Clearest omission — direct one-to-one, no schema conflict |
| `manage_task` | `TaskStop` (deferred) | |
| `manage_subagents` | `ListAgents` | |
| `send_message` | `SendMessage` | |
| `list_resources` | `ListMcpResourcesTool` | |
| `read_resource` | `ReadMcpResourceTool` | |
| `schedule` | `ScheduleWakeup`, `CronCreate` / `CronDelete` / `CronList` | one AGY target, several CC sources — injectivity conflict, see ADR 0004 §2 |

### Group C — no AGY equivalent; keep pass-through

`NotebookEdit`, `ReportFindings`, `EnterPlanMode` / `ExitPlanMode`, `EnterWorktree` /
`ExitWorktree`, `DeferredToolPlaceholder`, plus the MCP-resource and Agent-tool families
that AGY identifies as `(ServerName, ToolName)` pairs.

### Group D — no AGY equivalent and no CC equivalent

`generate_image`, `define_subagent`. Nothing to do.

---

## 4. Risks, Ranked

1. **`Glob -> list_dir` is a functional bug in production**, not a design preference.
   One-line fix at `main.go:3140`.
2. **No static identity inventory for CC.** `detectClient` (`main.go:4856`) falls to the
   generic branch, counting `cfg.ToolMappings["claude_code"]` directly with a floor of
   `minToolNameHits = 2` (`main.go:3337`) and no `clientDistinctiveTools` entry. Any body
   carrying two of `Bash` / `Read` / `Edit` is attributed to Claude Code. Codex and OMP
   both already have inventories; CC does not.
3. **No fail-closed protection for CC on `agy/*`.** The symmetry with OMP is missing:
   a CC request that cannot be safely cloaked is silently passed through rather than
   rejected.
4. **`Skill` / `Workflow` / `ToolSearch` map onto schema-incompatible targets**, so AGY
   can invoke the wrong capability rather than fail loudly.
5. **Brand restoration is one-directional.** `Claude Code -> Antigravity` on the request
   path only; assistant text saying `Antigravity` is not restored for CC the way
   `Antigravity -> omp` is for OMP.

---

## 5. Remediation Options

Ranked; option 1 is the recommended first pass.

1. **Minimal correctness fix (low risk).** Change `Glob -> find_by_name`; add the
   Group B mappings that are one-to-one (`read_url_content`, `manage_task`,
   `manage_subagents`, `send_message`, `list_resources`, `read_resource`); move
   `ToolSearch` / `Skill` / `Workflow` to pass-through. Roughly 10 lines at
   `main.go:3138`, plus test updates. Removes the functional break and the
   schema-mismatch class; leaves identity and protection untouched.
2. **Bring CC to parity with OMP.** Add a `claudeCodeSourceIdentityInventory` and a
   `clientDistinctiveTools` entry; extend `ProtectedAGY` and
   `requestsRequestScopedReverse` to CC on `agy/*`. Touches ~6 sites and needs its own
   ADR plus a verification checklist, following the shape of
   [ADR 0004](../adr/0004-cloak-codex-tool-names-by-wire-position.md) and
   [the Codex surface reference](codex-tool-surface-2026-09-12.md).
3. **Measure before deciding.** Run `tests/test_client.py` against a real Claude Code
   tool set to capture the actual wire declarations first, so the mapping decisions rest
   on observed names rather than on the documented CC surface.

Options 1 and 2 are independent: 1 is a correctness fix that should not wait on 2, and 2
is a behaviour change that needs evidence and an ADR.

---

## 6. Method & Confidence

- Every `main.go` reference above was read from revision `ff3479e` at the cited line.
- Tool semantics for the AGY side come from
  [the Antigravity tool surface reference](antigravity-tool-surface-2026-09-06.md)
  (20 native tools, verified 2026-09-12).
- Client-side CC tool names come from this session's own tool surface plus the
  documented table in [AGENTS.md](../../AGENTS.md) and [CONTEXT.md](../../CONTEXT.md).
- **Not verified:** no live CC request was captured during this audit. The Group A
  schema-mismatch findings are derived from the declared AGY parameter sets, not from an
  observed failed call. Capturing a real CC request/response pair is the first step of
  option 3 and would either confirm or downgrade those four entries.
- The `Agent` / `ListAgents` / `SendMessage` family ships as deferred tools in this
  Claude Code build, so a minimal session may declare a narrower tool set than the table
  assumes.
