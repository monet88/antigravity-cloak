# Tool Name Cloaking — Final Plan

## Bối cảnh

Plugin hiện tại chỉ replace brand name trong key `"system"`. Provider vẫn nhận diện client qua tool names đặc trưng. Plan này giả dạng Claude Code và Codex thành Antigravity bằng cách rename tool names.

## Design Decisions (Đã chốt)

| # | Quyết định | Kết quả |
|---|---|---|
| 1 | Name collision | Map mỗi tool vào tên Antigravity khác nhau từ pool chưa dùng |
| 2 | Schema mismatch | Chỉ đổi NAME, giữ nguyên schema/description gốc |
| 3 | Tool description | Mở rộng brand text replace cho `tools[].description` và `tools[].function.description` |
| 4 | Config approach | Hybrid: hardcode default + override qua config YAML |
| 5 | Unknown tools | Skip — giữ nguyên tên gốc, chỉ cloak tool đã biết |
| 6 | Request format | Dùng `SourceFormat` xác định OpenAI vs Anthropic format |
| 7 | Response state | Stateless — dùng `RequestBody` re-detect client mỗi lần |
| 8 | Performance | String replace trên raw JSON bytes cho response/stream chunk |
| 9 | tool_choice | Cloak luôn |
| 10 | System messages | Brand replace cho `messages[].content` với `role: system` |
| 11 | Scope | Phase 1: tool cloaking + brand expand. Phase 2 (sau): response brand reverse |

## Tool Name Mapping Tables

### Antigravity — Danh tính mục tiêu (tham chiếu)

```
ask_permission    ask_question       call_mcp_tool       define_subagent
generate_image    grep_search        invoke_subagent     list_dir
list_permissions  list_resources     manage_subagents    manage_task
multi_replace_file_content           read_resource       read_url_content
replace_file_content                 run_command         schedule
search_web        send_message       view_file           write_to_file
```

### Claude Code → Antigravity

| Claude Code | → Antigravity | Rationale |
|---|---|---|
| `bash` | `run_command` | Shell execution |
| `edit` | `replace_file_content` | File editing |
| `read` | `view_file` | File reading |
| `write` | `write_to_file` | File writing |
| `grep` | `grep_search` | Text search |
| `glob` | `list_dir` | File/dir listing |
| `agent` | `invoke_subagent` | Sub-agent delegation |
| `askUserQuestion` | `ask_question` | User interaction |
| `toolSearch` | `search_web` | Search capability |
| `skill` | `call_mcp_tool` | Extended capability |
| `workflow` | `schedule` | Task orchestration |

### Codex CLI → Antigravity

| Codex | → Antigravity | Rationale |
|---|---|---|
| `shell_command` | `run_command` | Shell execution |
| `apply_patch` | `multi_replace_file_content` | Multi-file editing |
| `request_user_input` | `ask_question` | User interaction |
| `view_image` | `generate_image` | Image handling |
| `update_plan` | `manage_task` | Task management |
| `tool_search` | `search_web` | Search capability |
| `get_goal` | `schedule` | Goal → task mapping (collision resolve) |
| `create_goal` | `send_message` | Collision resolve from pool |
| `update_goal` | `define_subagent` | Collision resolve from pool |
| `list_mcp_resources` | `list_resources` | Resource listing |
| `list_mcp_resource_templates` | `list_permissions` | Collision resolve from pool |
| `read_mcp_resource` | `read_resource` | Resource reading |

> [!NOTE]
> Antigravity requests detected → skip tool cloaking entirely (already native).

## Proposed Changes

---

### [MODIFY] [main.go](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go)

#### 1. Tool name mapping tables + reverse tables

```go
// Per-client cloak tables: original → Antigravity name
var defaultCloakTables = map[string]map[string]string{
    "claude_code": { /* ... */ },
    "codex":       { /* ... */ },
}

// Auto-built on init: Antigravity name → original (per client)
var defaultUncloakTables map[string]map[string]string
```

`init()` builds reverse tables from cloak tables.

#### 2. Client detection function

```go
func detectClient(toolNames []string) string
```

- Has `askUserQuestion` OR (`bash` + `edit` + `read`) → `"claude_code"`
- Has `shell_command` OR `apply_patch` → `"codex"`
- Has `ask_permission` OR `invoke_subagent` → `"antigravity"` (skip)
- Otherwise → `""` (skip)

#### 3. Tool name extraction helpers

```go
func extractToolNames(body map[string]any, sourceFormat string) []string
```

- Lấy danh sách tên từ mảng `tools`:
  - `sourceFormat == "openai"` → reads `tools[].function.name`
  - `sourceFormat == "anthropic"` → reads `tools[].name`
- **Fallback**: Nếu mảng `tools` trống, trích xuất tên tool từ lịch sử message để detect client:
  - `sourceFormat == "openai"` → quét `messages[].tool_calls[].function.name`
  - `sourceFormat == "anthropic"` → quét `messages[].content` (những block có `type == "tool_use"`) → lấy `.name`

#### 4. Expand `rewriteRequestBody` → add tool cloaking

After existing brand text replace:

1. Extract tool names → detect client
2. If client detected and has cloak table:
   - Rename tool names in `tools` array
   - Rename tool refs in `messages` (tool_calls, tool results)
   - Rename `tool_choice.function.name` if present
3. Apply brand text replace to `tools[].description` / `tools[].function.description`
4. Apply brand text replace to `messages[].content` where `role == "system"`

#### 5. Add response intercept handlers

```go
case pluginabi.MethodResponseInterceptAfter:
    return handleResponseIntercept(request), 0
case pluginabi.MethodResponseInterceptStreamChunk:
    return handleStreamChunkIntercept(request), 0
```

Both handlers:
1. Read `RequestBody` → extract tool names → detect client → get uncloak table
2. If no uncloak needed → return empty response (passthrough)
3. String-replace cloaked names back to originals in response `Body`
4. Return modified `Body`

#### 6. Update capabilities registration

```go
Capabilities: struct {
    ModelRouter             bool `json:"model_router"`
    Executor                bool `json:"executor"`
    RequestInterceptor      bool `json:"request_interceptor"`
    ResponseInterceptor     bool `json:"response_interceptor"`
    StreamChunkInterceptor  bool `json:"stream_chunk_interceptor"`
}{
    RequestInterceptor:     true,
    ResponseInterceptor:    true,
    StreamChunkInterceptor: true,
},
```

#### 7. Config: add `tool_mappings` field

New config field `tool_mappings` (type: object) for YAML override:

```yaml
tool_mappings:
  claude_code:
    new_tool_name: some_antigravity_tool
  codex:
    new_codex_tool: some_antigravity_tool
```

Merged on top of hardcoded defaults at reconfigure time.

---

### [MODIFY] [filter_test.go](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/filter_test.go)

New test cases:
- `TestRewriteRequestCloaksClaudeCodeToolNames` — `bash` → `run_command`, etc.
- `TestRewriteRequestCloaksCodexToolNames` — `shell_command` → `run_command`, etc.
- `TestRewriteRequestCloaksToolRefsInMessageHistory` — tool_calls renamed
- `TestRewriteRequestCloaksToolChoice` — tool_choice.function.name renamed
- `TestRewriteRequestSkipsAntigravityTools` — Antigravity detected → no changes
- `TestRewriteRequestSkipsUnknownTools` — unknown tool set → no tool cloaking
- `TestRewriteRequestAppliesBrandReplaceToToolDescription` — description brand replace
- `TestRewriteRequestAppliesBrandReplaceToSystemMessages` — system role messages

---

### [MODIFY] [plugin_test.go](file:///f:/CodeBase/cpa-plugin-antigravity-coding-filter/plugin_test.go)

New test cases:
- `TestHandlePluginCallRegisterDeclaresResponseInterceptor` — capabilities check
- `TestResponseInterceptReversesClaudeCodeCloak` — `run_command` → `bash` in response
- `TestResponseInterceptReversesCodexCloak` — `run_command` → `shell_command` in response  
- `TestStreamChunkInterceptReversesCloak` — stream chunk reverse
- `TestResponseInterceptPassesThroughAntigravity` — no reverse for Antigravity
- `TestReconfigureWithToolMappingsOverride` — YAML config override

## Verification Plan

### Automated Tests
```bash
cd f:\CodeBase\cpa-plugin-antigravity-coding-filter
go test -v -count=1 ./...
```

### Manual Verification
1. Build plugin, load vào CLIProxy
2. Claude Code request → provider sees Antigravity tool names
3. Claude Code receives original tool names back in response
4. Codex CLI request → same verification
5. Antigravity request → no changes applied
