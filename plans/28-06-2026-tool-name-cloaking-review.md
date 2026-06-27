# Scrutinize — Tool Name Cloaking Plan

## 1. Intent

**Goal**: Make Claude Code and Codex CLI requests appear to be Antigravity by renaming tool names in requests sent to the provider, and reversing those renames in responses sent back to the client.

**Is there a simpler alternative?**

No meaningfully simpler path exists. The current brand-text replace on `system` fields is insufficient because providers can identify clients by tool names. This is the logical next step — same intercept architecture, just widened to cover tool names + descriptions + message history. The plan correctly scopes to Phase 1 (request-side cloaking + response uncloak) without scope creep.

One consideration: **raw string replace on response bytes** (Decision #8) is an elegant shortcut, but is inherently fragile — see Findings #1 and #1b below.

---

## 2. Trace — Code Path Analysis

### Request flow (cloaking)

```
handlePluginCall("request.intercept_before")
  → handleRequestInterceptBefore(request)
    → rewriteRequestBody(req.Body)          ← currently takes only []byte, needs SourceFormat
      → [NEW] extract tool names via extractToolNames(body, sourceFormat)
      → [NEW] detectClient(toolNames)
      → [NEW] cloak tool names in tools[], messages[], tool_choice
      → [EXISTING] brand text replace on system fields
      → [NEW] brand text replace on tool descriptions      ← scope expansion
      → [NEW] brand text replace on system role messages   ← scope expansion
```

### Response flow (uncloaking)

```
handlePluginCall("response.intercept_after")
  → [NEW] handleResponseIntercept(request)
    → parse ResponseInterceptRequest (has RequestBody + Body)
    → extract tool names from RequestBody → detectClient → get uncloak table
    → string-replace cloaked names back to originals in Body
    → return ResponseInterceptResponse{Body: modified}

handlePluginCall("response.intercept_stream_chunk")
  → [NEW] handleStreamChunkIntercept(request)
    → parse StreamChunkInterceptRequest (has RequestBody + Body)
    → same detection + string-replace logic
    → return StreamChunkInterceptResponse{Body: modified}
```

### Registration

Current [registrationResponse()](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L145-L172) uses inline struct with only `RequestInterceptor: true`. Plan adds `ResponseInterceptor` and `StreamChunkInterceptor`.

### Current method dispatch

[handlePluginCall](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L119-L132) currently handles 4 cases. `request.intercept_after` is a stub returning empty response (line 127-128). All other methods fall to `default` → error. Plan adds 2 new cases but doesn't mention what happens to the `request.intercept_after` stub.

---

## 3. Verify — Findings

---

### 🔴 High #1 — String replace on response bytes causes false positives (content corruption)

**Finding**: The plan specifies "string-replace cloaked names back to originals in response `Body`" (line 141). Cloaked names like `run_command`, `view_file`, `list_dir` are **common English fragments** that could appear in LLM-generated prose, code snippets, tool output, arguments, error text, or user messages.

**Example**: Provider response contains `"You can run_command to execute..."` in assistant text. The uncloak replace would turn `run_command` → `bash` (for Claude Code), mangling the response: `"You can bash to execute..."`.

**Why it matters**: Silent corruption of LLM output. No error, just wrong text delivered to the client.

**Evidence**: Decision #8 + the cloak table maps common names: `run_command` ← `bash`/`shell_command`, `view_file` ← `read`, `list_dir` ← `glob`. Current codebase only parses JSON structurally when rewriting requests ([rewriteRequestBody](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L395-L409)).

**Suggested change**: Instead of raw string replace, do **structured JSON replace** on response body. Target only tool-name-bearing fields:
- Non-streaming: parse JSON → replace `tool_calls[].function.name` (OpenAI) / content blocks with `type: "tool_use"` `.name` (Anthropic) → re-serialize.
- Streaming: for SSE data lines that are valid JSON chunks, parse and replace only in tool-name fields. Pass through non-JSON lines untouched.
- If perf is a concern, a quick `bytes.Contains` check for any cloaked name can skip parsing entirely for chunks that don't contain tool names.

---

### 🔴 High #1b — Stream fragment risk: tool names can split across SSE chunks

**Finding**: Tool names may be fragmented across streaming chunks, particularly OpenAI SSE `function.name` + `arguments` deltas. A global `strings.Replace` of `"run_command"` will **miss** if a chunk boundary splits the name (e.g., chunk contains only `"run_com"`).

**Why it matters**: Missed uncloak → client receives Antigravity tool name → client doesn't recognise it → potential crash or undefined behavior.

**Evidence**: OpenAI SSE typically delivers `function.name` complete in the first delta chunk and `arguments` as subsequent deltas, so the name is *usually* intact. But this is an **assumption, not a guarantee** — the SSE spec doesn't mandate it, and proxy/CDN rebuffering could re-fragment.

**Suggested change**: Add explicit note in plan about this assumption. Consider either:
1. **Targeted field replace** (solving both #1 and #1b) — parse each SSE `data:` line as JSON, replace only in `tool_calls[].function.name` / `tool_use` name fields.
2. **Buffer approach** — accumulate partial tool_call name across chunks before replacing. More complex, less recommended.

---

### 🟡 High #2 — Brand replace scope expansion is an undocumented behavior change

**Finding**: Current code [rewriteSystemFields](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L411-L443) **only** touches the `"system"` key. [TestRewriteRequestIgnoresKeywordsOutsideSystem](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/filter_test.go#L45-L54) explicitly asserts that brand keywords in `messages` and `input` are **not** rewritten. The README confirms "chỉ trong system".

The plan expands brand replace to:
- `tools[].description` / `tools[].function.description`
- `messages[].content` where `role == "system"`

**Why it matters**: This breaks the existing contract tested at [filter_test.go:45-54](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/filter_test.go#L45-L54). Existing tests will either fail or become misleading. README becomes stale.

**Suggested change**: 
1. Add explicit "mở rộng scope brand replace" in Design Decisions table.
2. Update or deprecate `TestRewriteRequestIgnoresKeywordsOutsideSystem` to reflect the new scope.
3. Update README after implementation.

---

### 🟡 High #3 — Implementation details missing at critical junctures

Several concrete signatures and edge cases are described at idea level but lack implementation specifics:

**3a. `rewriteRequestBody` needs `SourceFormat`**

Current signature: `func rewriteRequestBody(body []byte) ([]byte, bool)` ([main.go:395](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L395)). But [extractToolNames](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/plans/28-06-2026-tool-name-cloaking.md#L107) needs `sourceFormat` to know `tools[].function.name` (OpenAI) vs `tools[].name` (Anthropic). The caller [handleRequestInterceptBefore](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L189-L200) has `req.SourceFormat` available but currently only passes `req.Body`.

**3b. Cloak functions not specified**

The actual mutations — renaming in `tools[]`, `messages[].tool_calls[].function.name`, Anthropic `tool_use` blocks, `tool_choice.function.name` — are listed as bullets but no function signatures or pseudocode.

**3c. Response handlers must return proper envelope types**

Plan line 132-136 shows `handleResponseIntercept(request)` and `handleStreamChunkIntercept(request)`. These must return `mustEnvelope(pluginapi.ResponseInterceptResponse{Body: ...})` and `mustEnvelope(pluginapi.StreamChunkInterceptResponse{Body: ...})` respectively — not raw bytes or empty envelopes.

**3d. `tool_choice` has multiple shapes**

`tool_choice` can be:
- A **string**: `"auto"`, `"required"`, `"none"` (OpenAI) / `"auto"`, `"any"`, `"tool"` (Anthropic)
- An **object**: `{type: "function", function: {name: "..."}}` (OpenAI) / `{type: "tool", name: "..."}` (Anthropic)

The plan only mentions `tool_choice.function.name`. String values must be handled safely (skip, don't crash).

**3e. `parseFilterConfigYAML` + `tool_mappings` merge logic**

No specifics on how `tool_mappings` YAML is parsed and merged into `defaultCloakTables`. Override vs additive semantics unclear.

**Suggested change**: Add function signatures and edge case handling for each of these in the plan.

---

### 🟡 High #4 — Capabilities registration + existing test will break

**Finding**: Both the inline struct definition ([main.go:149-153](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L149-L153)) and the value assignment ([main.go:164-170](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L164-L170)) in `registrationResponse()` must be updated to add `ResponseInterceptor` and `StreamChunkInterceptor` fields.

[TestHandlePluginCallRegisterDeclaresRequestInterceptor](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/plugin_test.go#L10-L43) currently asserts only `request_interceptor: true`. After the change, this test will **pass but be incomplete** — it won't verify the new capabilities are declared.

**Suggested change**: Plan should explicitly note the test update. Add assertions for `response_interceptor: true` and `stream_chunk_interceptor: true`.

---

### 🟡 High #5 — Config YAML example is ambiguous

**Finding**: Plan line 165-170 shows:

```yaml
tool_mappings:
  claude_code:
    new_tool_name: some_antigravity_tool
```

Is `new_tool_name` the **original** tool name (key) mapping to an Antigravity target (value)? Or the reverse? The naming `new_tool_name` suggests it's a new tool being mapped, but the structure should be `{orig_name: antigravity_target_name}` to match the cloak table semantics.

**Suggested change**: Clarify with a concrete example:
```yaml
tool_mappings:
  claude_code:
    bash: run_command        # orig_tool_name: antigravity_target_name
  codex:
    shell_command: run_command
```

---

### 🟡 Major #6 — `changed` flag doesn't aggregate new mutations

**Finding**: The current [rewriteRequestBody](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L395-L409) parses JSON → calls `rewriteSystemFields` → returns early if `!changed` at line 401. The plan adds brand replace on `tools[].description` and `messages[].content` where `role == "system"`.

**Why it matters**: If the only change is a brand replace inside a tool description (no tool cloaking needed, no system field change), the current flow's early return at line 401 would skip re-serialization.

**Evidence**: [main.go:400-401](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L400-L401) — `changed` comes solely from `rewriteSystemFields`.

**Suggested change**: Ensure the implementation ORs all mutation results (tool cloaking + description brand replace + system message brand replace) into a single `changed` flag before the early return check.

---

### 🟡 Major #7 — Per-chunk `RequestBody` re-parse overhead

**Finding**: Both response handlers re-parse `RequestBody` to detect client and build uncloak table for **every** response or stream chunk.

**Why it matters**: For a streaming response with hundreds of chunks, this repeats `json.Unmarshal` + `extractToolNames` + `detectClient` on the same `RequestBody` each time. Stateless design (Decision #7) is consistent but pays a per-chunk tax. With tool-heavy flows and large request bodies, this may become measurable.

**Evidence**: `StreamChunkInterceptRequest.RequestBody` is the same bytes every chunk. No benchmarks exist in the current codebase.

**Suggested change**: Accept the overhead as intentional simplicity — just document the tradeoff. Or add lightweight cache keyed by request hash.

---

### 🟢 Low #8 — `request.intercept_after` stub left unaddressed

**Finding**: [handlePluginCall](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go#L127-L128) currently has a stub for `request.intercept_after` returning empty `RequestInterceptResponse{}`. The plan doesn't mention whether this should be updated or left as-is.

**Suggested change**: Explicitly decide: keep as no-op stub, or implement tool cloaking there too (for post-credential-selection requests). Document the decision.

---

### 🟢 Low #9 — Collision resolve mappings need code comments

**Finding**: Codex tools `get_goal` → `schedule`, `create_goal` → `send_message`, `update_goal` → `define_subagent`, `list_mcp_resource_templates` → `list_permissions` are pool-allocated collision resolvers, not semantic matches.

**Suggested change**: Document in code comments. Already clear in the plan table, should survive into implementation.

---

### 🟢 Low #10 — Message history fallback extraction needs test coverage

**Finding**: `extractToolNames` fallback to scanning message history when `tools[]` is empty is a good design. But the plan's test list doesn't include a case for `tools` array empty + only history present.

**Suggested change**: Add test case: request with no `tools` array, only `messages` containing `tool_calls` → verify correct client detection.

---

### 🟢 Low #11 — Antigravity detection as skip gate needs strong test coverage

**Finding**: `detectClient` returning `"antigravity"` causes the entire tool cloaking pipeline to be skipped. This is the safety gate preventing Antigravity-native requests from being mangled.

**Suggested change**: Test with edge cases: Antigravity tool names mixed with unknown tools, partial Antigravity tool sets, etc.

---

### 🟢 Low #12 — Tests lack Anthropic format coverage

**Finding**: The existing test helper [requestInterceptRequestJSON](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/plugin_test.go#L212-L225) always sends `SourceFormat: "openai"`. The new tests need variants with `SourceFormat: "anthropic"` to exercise the Anthropic tool format paths (`tools[].name` vs `tools[].function.name`).

**Suggested change**: Add test cases with `SourceFormat: "anthropic"` for both Claude Code and Codex cloaking paths.

---

### 🟢 Low #13 — Config merge semantics unspecified

**Finding**: Plan line 172 says "Merged on top of hardcoded defaults at reconfigure time." But doesn't specify: does a YAML override **add** new mappings, or can it also **remove/disable** a default mapping?

**Suggested change**: Specify: YAML overrides win for the same source tool name. Document this in plan.

---

## 4. Architecture Impact & Risks

| Aspect | Impact |
|--------|--------|
| **Surface area** | Plugin now uses request + response + stream interceptors. Host must enable all three. |
| **Performance** | `json.Unmarshal` on `RequestBody` (can be large) per chunk + per response. No benchmarks exist. Acceptable for now but should be monitored. |
| **Correctness risk** | If uncloak misses any tool name → client receives Antigravity name → client confusion / crash on tool result processing. |
| **Backward compat** | No breaking changes if implemented correctly. Antigravity-native requests skip, unknown tools preserved. |
| **Scope discipline** | Remains "filter/rewrite" only. No model_router/executor regression. ✅ |

---

## 5. Verdict

**Fix-then-ship.**

The two highest-priority issues are **#1 and #1b**: raw string replace on response bytes will produce false positives AND may miss fragmented tool names in streams. Switching to **structured JSON replace on tool-name-bearing fields** solves both problems. Finding #2 (brand replace scope expansion) needs explicit documentation as a behavior change. Finding #3 (missing implementation signatures) should be fleshed out before coding to avoid ambiguity.

Everything else is solid — the design decisions are well-reasoned, the mapping tables are complete, and the stateless response interception via `RequestBody` is correct per the SDK.

| # | Severity | Finding | Action |
|---|----------|---------|--------|
| 1 | 🔴 High | String replace on response bytes causes false positives | Switch to structured JSON field replace |
| 1b | 🔴 High | Stream fragments can split tool names | Targeted field replace solves both #1 and #1b |
| 2 | 🟡 High | Brand replace scope expansion is undocumented behavior change | Add to Design Decisions, update tests + README |
| 3 | 🟡 High | Implementation details missing (signatures, edge cases) | Flesh out in plan |
| 4 | 🟡 High | Capabilities + existing test will break | Note test updates explicitly |
| 5 | 🟡 High | Config YAML example is ambiguous | Clarify with concrete example |
| 6 | 🟡 Major | `changed` flag doesn't aggregate new mutations | OR all mutation results |
| 7 | 🟡 Major | Per-chunk `RequestBody` re-parse overhead | Document tradeoff |
| 8 | 🟢 Low | `request.intercept_after` stub unaddressed | Decide and document |
| 9 | 🟢 Low | Collision-resolve mappings need comments | Add code comments |
| 10 | 🟢 Low | Fallback extraction needs test | Add empty-tools test case |
| 11 | 🟢 Low | Antigravity skip gate needs strong tests | Edge case coverage |
| 12 | 🟢 Low | Tests lack Anthropic format coverage | Add `SourceFormat: "anthropic"` tests |
| 13 | 🟢 Low | Config merge semantics unspecified | Document override-wins |
