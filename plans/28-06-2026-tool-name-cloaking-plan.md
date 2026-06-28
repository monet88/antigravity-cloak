# Tool Name Cloaking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement tool name cloaking to disguise Claude Code and Codex CLI as Antigravity by mapping their specific tool names.

**Architecture:** Add static cloak mapping tables. Intercept requests to detect the client based on tool signatures (including fallback in history), cloak tool names in the request payload. Intercept responses and stream chunks to uncloak the tool names back to the original client's tools.

**Tech Stack:** Go, `encoding/json`, `pluginabi`

## Global Constraints

- Go standard library + `gopkg.in/yaml.v3` only.
- Dùng `SourceFormat` xác định OpenAI vs Anthropic format
- Stateless — dùng `RequestBody` re-detect client mỗi lần (chấp nhận per-chunk re-parse overhead cho đơn giản)
- Response uncloak: **structured JSON field replace** — parse tool-name-bearing fields only (KHÔNG dùng raw string replace trên response bytes để tránh false positives và stream fragment risk)

## Design Decision Updates

> [!IMPORTANT]
> **Decision #8 (revised):** Dùng structured JSON replace cho response/stream uncloak thay vì raw string replace.
> Lý do: raw replace gây false positives (tên tool như `run_command` xuất hiện trong prose) và miss stream fragments.
> Non-streaming: parse JSON → replace chỉ trong `tool_calls[].function.name` (OpenAI) / content blocks `type: "tool_use"` `.name` (Anthropic).
> Streaming: parse mỗi SSE `data:` line JSON → replace chỉ trong tool-name fields. Non-JSON lines passthrough.

> [!IMPORTANT]
> **Decision #3 (scope expansion acknowledged):** Mở rộng brand text replace từ chỉ `"system"` key sang `tools[].description` / `tools[].function.description` và `messages[].content` với `role == "system"`.
> Đây là behavior change — test `TestRewriteRequestIgnoresKeywordsOutsideSystem` cần update. README cần update sau implementation.

---

### Task 1: Update Capabilities, Config Schema, and Tool Mappings Parse

**Files:**
- Modify: `main.go`
- Modify: `plugin_test.go`

**Interfaces:**
- Consumes: `pluginapi.Metadata`, `pluginabi.Capabilities`
- Produces: Updated capability flags, config struct, tool_mappings parse logic

- [ ] **Step 1: Write the failing test**

```go
// Update existing TestHandlePluginCallRegisterDeclaresRequestInterceptor in plugin_test.go
// ADD assertions for new capabilities:
capabilities := result["capabilities"].(map[string]any)
if capabilities["response_interceptor"] != true {
    t.Fatalf("response_interceptor = %#v, want true", capabilities["response_interceptor"])
}
if capabilities["stream_chunk_interceptor"] != true {
    t.Fatalf("stream_chunk_interceptor = %#v, want true", capabilities["stream_chunk_interceptor"])
}

// ADD assertion for new config field:
if !hasConfigField(fields, "tool_mappings", "object") {
    t.Fatalf("ConfigFields = %#v, want object tool_mappings", fields)
}

// ADD new test for tool_mappings reconfigure:
func TestReconfigureWithToolMappingsOverride(t *testing.T) {
    defer restoreDefaultFilterConfig(t)
    raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
tool_mappings:
  claude_code:
    bash: run_command
    my_custom_tool: ask_permission
  codex:
    shell_command: run_command
`)))
    if code != 0 { t.Fatalf("code = %d, want 0; body=%s", code, raw) }
    cfg := activeFilterConfig()
    // Verify ToolMappings parsed correctly
    if cfg.ToolMappings["claude_code"]["my_custom_tool"] != "ask_permission" {
        t.Fatalf("expected custom tool mapping")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run "TestHandlePluginCallRegisterDeclaresRequestInterceptor|TestReconfigureWithToolMappingsOverride" -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// In configFields() — ADD new field:
{
    Name:        "tool_mappings",
    Type:        pluginapi.ConfigFieldTypeObject,
    Description: "Custom tool name mappings per client. Keys: client name (claude_code, codex). Values: map of original_tool_name → antigravity_target_name. Overrides defaults for matching keys.",
},

// In registrationResponse() — UPDATE Capabilities struct:
Capabilities: struct {
    ModelRouter            bool `json:"model_router"`
    Executor               bool `json:"executor"`
    RequestInterceptor     bool `json:"request_interceptor"`
    ResponseInterceptor    bool `json:"response_interceptor"`
    StreamChunkInterceptor bool `json:"stream_chunk_interceptor"`
}{
    RequestInterceptor:     true,
    ResponseInterceptor:    true,
    StreamChunkInterceptor: true,
},

// In filterConfig struct — ADD ToolMappings:
type filterConfig struct {
    UseDefaultKeywords bool
    CustomMappings     []rewriteMapping
    ToolMappings       map[string]map[string]string  // client → {orig_tool: antigravity_tool}
}

// In parseFilterConfigYAML — ADD tool_mappings parsing:
if value, exists := values["tool_mappings"]; exists {
    // Parse as map[string]map[string]string
    // Override-wins semantics: YAML keys override defaults for same orig_tool_name
    // Keys not present in YAML keep their default values
}
```

**Config YAML format (clarified):**
```yaml
tool_mappings:
  claude_code:
    bash: run_command            # orig_tool_name: antigravity_target_name
    my_custom_tool: ask_permission  # add new mapping
  codex:
    shell_command: run_command   # override or confirm default
```
Override-wins semantics: YAML values override defaults for the same `orig_tool_name`. Default mappings not mentioned in YAML are preserved.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run "TestHandlePluginCallRegisterDeclaresRequestInterceptor|TestReconfigureWithToolMappingsOverride" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go plugin_test.go
git commit -m "feat: add capabilities and config for tool cloaking"
```

### Task 2: Tool Name Mapping Tables and Reverse Maps

**Files:**
- Modify: `main.go`
- Modify: `filter_test.go`

**Interfaces:**
- Consumes: N/A
- Produces: `defaultCloakTables`, `defaultUncloakTables`

- [ ] **Step 1: Write the failing test**

```go
// Add to filter_test.go
func TestUncloakTablesInitialization(t *testing.T) {
    if len(defaultUncloakTables) == 0 {
        t.Fatal("uncloak tables not initialized")
    }
    // Claude Code
    if defaultUncloakTables["claude_code"]["run_command"] != "bash" {
        t.Fatal("expected bash")
    }
    // Codex
    if defaultUncloakTables["codex"]["run_command"] != "shell_command" {
        t.Fatal("expected shell_command")
    }
    // Verify no key collision within a client's cloak table
    for client, cloaks := range defaultCloakTables {
        seen := make(map[string]bool)
        for _, target := range cloaks {
            if seen[target] {
                t.Fatalf("client %s has duplicate target %s", client, target)
            }
            seen[target] = true
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestUncloakTablesInitialization -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
var defaultCloakTables = map[string]map[string]string{
    "claude_code": {
        "bash": "run_command", "edit": "replace_file_content", "read": "view_file",
        "write": "write_to_file", "grep": "grep_search", "glob": "list_dir",
        "agent": "invoke_subagent", "askUserQuestion": "ask_question",
        "toolSearch": "search_web", "skill": "call_mcp_tool", "workflow": "schedule",
    },
    "codex": {
        "shell_command": "run_command", "apply_patch": "multi_replace_file_content",
        "request_user_input": "ask_question", "view_image": "generate_image",
        "update_plan": "manage_task", "tool_search": "search_web",
        // Collision-resolve: these map to unused Antigravity pool names since
        // the semantic match is imperfect. The goal is unique names only.
        "get_goal": "schedule",
        "create_goal": "send_message",
        "update_goal": "define_subagent",
        "list_mcp_resources": "list_resources",
        "list_mcp_resource_templates": "list_permissions",  // collision resolve from pool
        "read_mcp_resource": "read_resource",
    },
}

var defaultUncloakTables map[string]map[string]string

func init() {
    defaultUncloakTables = make(map[string]map[string]string)
    for client, cloaks := range defaultCloakTables {
        uncloaks := make(map[string]string)
        for orig, mapped := range cloaks {
            uncloaks[mapped] = orig
        }
        defaultUncloakTables[client] = uncloaks
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestUncloakTablesInitialization -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go filter_test.go
git commit -m "feat: setup cloak and uncloak tables"
```

### Task 3: Client Detection Logic

**Files:**
- Modify: `main.go`
- Modify: `filter_test.go`

**Interfaces:**
- Consumes: JSON body, SourceFormat
- Produces: `detectClient`, `extractToolNames`

- [ ] **Step 1: Write the failing test**

```go
// Add to filter_test.go
func TestDetectClient(t *testing.T) {
    tests := []struct {
        name         string
        toolNames    []string
        wantClient   string
    }{
        {"claude code by askUserQuestion", []string{"bash", "askUserQuestion", "read"}, "claude_code"},
        {"claude code by signature trio", []string{"bash", "edit", "read", "write"}, "claude_code"},
        {"codex by shell_command", []string{"shell_command", "apply_patch"}, "codex"},
        {"codex by apply_patch only", []string{"apply_patch", "request_user_input"}, "codex"},
        {"antigravity by ask_permission", []string{"ask_permission", "run_command"}, "antigravity"},
        {"antigravity by invoke_subagent", []string{"invoke_subagent", "view_file"}, "antigravity"},
        {"unknown tools", []string{"custom_tool", "another_tool"}, ""},
        {"empty list", []string{}, ""},
        // Edge case: Antigravity tools mixed with Claude-like names → Antigravity wins
        {"antigravity mixed with claude-like", []string{"bash", "ask_permission"}, "antigravity"},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := detectClient(tt.toolNames)
            if got != tt.wantClient {
                t.Fatalf("detectClient(%v) = %q, want %q", tt.toolNames, got, tt.wantClient)
            }
        })
    }
}

func TestExtractToolNames(t *testing.T) {
    tests := []struct {
        name         string
        body         string
        sourceFormat string
        wantLen      int  // minimum expected tool names
    }{
        {
            name: "openai tools array",
            body: `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}}]}`,
            sourceFormat: "openai",
            wantLen: 2,
        },
        {
            name: "anthropic tools array",
            body: `{"tools":[{"name":"bash","description":"run shell"},{"name":"read","description":"read file"}]}`,
            sourceFormat: "anthropic",
            wantLen: 2,
        },
        {
            name: "openai fallback to message history tool_calls",
            body: `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"bash"}}]}]}`,
            sourceFormat: "openai",
            wantLen: 1,
        },
        {
            name: "anthropic fallback to message history tool_use",
            body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"bash"}]}]}`,
            sourceFormat: "anthropic",
            wantLen: 1,
        },
        {
            name: "empty tools and no history",
            body: `{"messages":[{"role":"user","content":"hello"}]}`,
            sourceFormat: "openai",
            wantLen: 0,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var parsed map[string]any
            if err := json.Unmarshal([]byte(tt.body), &parsed); err != nil {
                t.Fatalf("unmarshal: %v", err)
            }
            names := extractToolNames(parsed, tt.sourceFormat)
            if len(names) < tt.wantLen {
                t.Fatalf("extractToolNames got %d names %v, want >= %d", len(names), names, tt.wantLen)
            }
        })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run "TestDetectClient|TestExtractToolNames" -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
func extractToolNames(body map[string]any, sourceFormat string) []string {
    var names []string

    // Check tools array first
    if toolsRaw, ok := body["tools"].([]any); ok {
        for _, tRaw := range toolsRaw {
            if tMap, ok := tRaw.(map[string]any); ok {
                if sourceFormat == "openai" {
                    if fn, ok := tMap["function"].(map[string]any); ok {
                        if name, ok := fn["name"].(string); ok {
                            names = append(names, name)
                        }
                    }
                } else if sourceFormat == "anthropic" {
                    if name, ok := tMap["name"].(string); ok {
                        names = append(names, name)
                    }
                }
            }
        }
    }

    // Fallback to history when tools[] is empty or absent
    if len(names) == 0 {
        if msgsRaw, ok := body["messages"].([]any); ok {
            for _, mRaw := range msgsRaw {
                if msg, ok := mRaw.(map[string]any); ok {
                    if sourceFormat == "openai" {
                        if calls, ok := msg["tool_calls"].([]any); ok {
                            for _, cRaw := range calls {
                                if call, ok := cRaw.(map[string]any); ok {
                                    if fn, ok := call["function"].(map[string]any); ok {
                                        if name, ok := fn["name"].(string); ok {
                                            names = append(names, name)
                                        }
                                    }
                                }
                            }
                        }
                    } else if sourceFormat == "anthropic" {
                        if contents, ok := msg["content"].([]any); ok {
                            for _, cntRaw := range contents {
                                if cnt, ok := cntRaw.(map[string]any); ok {
                                    if cnt["type"] == "tool_use" {
                                        if name, ok := cnt["name"].(string); ok {
                                            names = append(names, name)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
    return names
}

func detectClient(toolNames []string) string {
    hasClaude := false
    hasCodex := false
    hasAntigravity := false

    claudeSigs := map[string]bool{"bash": true, "edit": true, "read": true}
    claudeMatchCount := 0

    for _, n := range toolNames {
        if n == "askUserQuestion" {
            hasClaude = true
        }
        if claudeSigs[n] {
            claudeMatchCount++
        }
        if n == "shell_command" || n == "apply_patch" {
            hasCodex = true
        }
        if n == "ask_permission" || n == "invoke_subagent" {
            hasAntigravity = true
        }
    }

    // Antigravity detection takes priority — skip cloaking entirely
    if hasAntigravity {
        return "antigravity"
    }
    if hasClaude || claudeMatchCount >= 3 {
        return "claude_code"
    }
    if hasCodex {
        return "codex"
    }
    return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run "TestDetectClient|TestExtractToolNames" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go filter_test.go
git commit -m "feat: detect client from tools or history"
```

### Task 4: Cloak Request Payload

**Files:**
- Modify: `main.go`
- Modify: `filter_test.go`

**Interfaces:**
- Consumes: `detectClient`, `defaultCloakTables`, `sourceFormat`
- Produces: Rewritten JSON body in `rewriteRequestBody`

**Critical changes:**
1. `rewriteRequestBody` signature changes to `func rewriteRequestBody(body []byte, sourceFormat string) ([]byte, bool)` — needs `sourceFormat` for `extractToolNames`
2. `handleRequestInterceptBefore` passes `req.SourceFormat` to `rewriteRequestBody`
3. `changed` flag aggregates all mutations (system rewrite + tool cloaking + description brand replace + system message brand replace)
4. Brand replace scope expands to `tools[].description` / `tools[].function.description` and `messages[].content` where `role == "system"`

- [ ] **Step 1: Write the failing test**

```go
// Add to filter_test.go
func TestRewriteRequestBodyCloaksClaudeCodeTools(t *testing.T) {
    // OpenAI format with Claude Code tools
    body := `{
        "system":"You are Claude Code.",
        "tools":[
            {"type":"function","function":{"name":"bash","description":"Run Claude Code shell commands"}},
            {"type":"function","function":{"name":"read","description":"Read files"}}
        ],
        "messages":[],
        "tool_choice":{"type":"function","function":{"name":"bash"}}
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "openai")
    if !rewritten { t.Fatal("want rewritten") }
    var parsed map[string]any
    json.Unmarshal(got, &parsed)
    // Assert tools[0].function.name == "run_command"
    // Assert tools[1].function.name == "view_file"
    // Assert tool_choice.function.name == "run_command"
    // Assert description brand replace: "Claude Code" → "Antigravity" in description
    // Assert system field brand replace
}

func TestRewriteRequestBodyCloaksCodexTools(t *testing.T) {
    body := `{
        "system":"You are Codex.",
        "tools":[
            {"type":"function","function":{"name":"shell_command","description":"Execute Codex shell"}},
            {"type":"function","function":{"name":"apply_patch","description":"Apply patches"}}
        ],
        "messages":[]
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "openai")
    if !rewritten { t.Fatal("want rewritten") }
    // Assert tools[0].function.name == "run_command"
    // Assert tools[1].function.name == "multi_replace_file_content"
}

func TestRewriteRequestBodyCloaksToolRefsInMessageHistory(t *testing.T) {
    body := `{
        "tools":[{"type":"function","function":{"name":"bash"}}],
        "messages":[
            {"role":"assistant","tool_calls":[{"id":"tc1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
            {"role":"tool","tool_call_id":"tc1","name":"bash","content":"output"}
        ]
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "openai")
    if !rewritten { t.Fatal("want rewritten") }
    // Assert messages[0].tool_calls[0].function.name == "run_command"
    // Assert messages[1].name == "run_command"
}

func TestRewriteRequestBodyHandlesToolChoiceShapes(t *testing.T) {
    tests := []struct{
        name string
        body string
    }{
        {
            name: "tool_choice as string (skip safely)",
            body: `{"tools":[{"type":"function","function":{"name":"bash"}}],"tool_choice":"auto","messages":[]}`,
        },
        {
            name: "tool_choice as object with function.name",
            body: `{"tools":[{"type":"function","function":{"name":"bash"}}],"tool_choice":{"type":"function","function":{"name":"bash"}},"messages":[]}`,
        },
        {
            name: "anthropic tool_choice as object with name",
            body: `{"tools":[{"name":"bash"}],"tool_choice":{"type":"tool","name":"bash"},"messages":[]}`,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            sourceFormat := "openai"
            if strings.Contains(tt.name, "anthropic") { sourceFormat = "anthropic" }
            _, rewritten := rewriteRequestBody([]byte(tt.body), sourceFormat)
            if !rewritten { t.Fatal("want rewritten") }
            // string tool_choice should remain unchanged
            // object tool_choice should have name cloaked
        })
    }
}

func TestRewriteRequestBodySkipsAntigravityTools(t *testing.T) {
    body := `{
        "tools":[{"type":"function","function":{"name":"ask_permission"}},{"type":"function","function":{"name":"run_command"}}],
        "messages":[]
    }`
    _, rewritten := rewriteRequestBody([]byte(body), "openai")
    // No tool cloaking needed, but may still have brand replace
    // Assert tool names unchanged
}

func TestRewriteRequestBodySkipsUnknownTools(t *testing.T) {
    body := `{
        "tools":[{"type":"function","function":{"name":"custom_tool"}},{"type":"function","function":{"name":"another_tool"}}],
        "messages":[]
    }`
    _, rewritten := rewriteRequestBody([]byte(body), "openai")
    if rewritten { t.Fatal("want no rewrite for unknown tools") }
}

func TestRewriteRequestBodyAppliesBrandReplaceToToolDescription(t *testing.T) {
    body := `{
        "tools":[{"type":"function","function":{"name":"bash","description":"Claude Code shell tool"}}],
        "messages":[]
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "openai")
    if !rewritten { t.Fatal("want rewritten") }
    // Assert description contains "Antigravity" not "Claude Code"
}

func TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages(t *testing.T) {
    body := `{
        "tools":[{"type":"function","function":{"name":"bash"}}],
        "messages":[
            {"role":"system","content":"You are Claude Code assistant."},
            {"role":"user","content":"hello Claude Code"}
        ]
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "openai")
    if !rewritten { t.Fatal("want rewritten") }
    // Assert messages[0].content → brand replaced (role == "system")
    // Assert messages[1].content → NOT replaced (role == "user")
}

// UPDATE existing test to reflect new scope:
func TestRewriteRequestIgnoresKeywordsOutsideSystem(t *testing.T) {
    // UPDATED: This test now verifies brand replace does NOT touch
    // user/assistant message content. System role messages ARE replaced (new behavior).
    body := []byte(`{
        "messages":[
            {"role":"user","content":"please compare OpenCode and Codex"},
            {"role":"assistant","content":"Claude Code is a tool"}
        ],
        "input":"Claude Code is mentioned by the user"
    }`)
    got, rewritten := rewriteRequestBody(body, "openai")
    if rewritten {
        t.Fatalf("rewritten = true, want false; body=%s", got)
    }
}

// Anthropic format tests
func TestRewriteRequestBodyCloaksAnthropicFormat(t *testing.T) {
    body := `{
        "system":"You are Claude Code.",
        "tools":[
            {"name":"bash","description":"Run Claude Code shell"},
            {"name":"read","description":"Read files"}
        ],
        "messages":[
            {"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"bash","input":{}}]},
            {"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]}
        ]
    }`
    got, rewritten := rewriteRequestBody([]byte(body), "anthropic")
    if !rewritten { t.Fatal("want rewritten") }
    // Assert tools[0].name == "run_command"
    // Assert tools[1].name == "view_file"
    // Assert messages[0].content[0].name == "run_command" (tool_use block)
    // Assert description brand replace
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run "TestRewriteRequestBody|TestRewriteRequestIgnoresKeywordsOutsideSystem" -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// UPDATE signature — add sourceFormat parameter:
func rewriteRequestBody(body []byte, sourceFormat string) ([]byte, bool) {
    var root map[string]any
    if err := json.Unmarshal(body, &root); err != nil {
        return nil, false
    }

    changed := false

    // 1. Existing brand text replace on "system" key
    mappings := effectiveMappings(activeFilterConfig())
    rewritten, sysChanged := rewriteSystemFields(root, mappings)
    root = rewritten.(map[string]any)
    changed = changed || sysChanged

    // 2. Tool cloaking
    toolNames := extractToolNames(root, sourceFormat)
    client := detectClient(toolNames)
    if client != "" && client != "antigravity" {
        cloakTable := effectiveCloakTable(client) // merge defaults + config overrides
        toolCloaked := cloakToolNames(root, cloakTable, sourceFormat)
        changed = changed || toolCloaked
    }

    // 3. Brand replace in tools[].description / tools[].function.description
    descChanged := rewriteToolDescriptions(root, mappings, sourceFormat)
    changed = changed || descChanged

    // 4. Brand replace in messages[].content where role == "system"
    sysMsgChanged := rewriteSystemMessages(root, mappings)
    changed = changed || sysMsgChanged

    if !changed {
        return nil, false
    }
    raw, err := json.Marshal(root)
    if err != nil {
        return nil, false
    }
    return raw, true
}

// UPDATE handleRequestInterceptBefore to pass sourceFormat:
func handleRequestInterceptBefore(request []byte) []byte {
    var req pluginapi.RequestInterceptRequest
    if err := json.Unmarshal(request, &req); err != nil {
        return mustErrorEnvelope("invalid_request", ...)
    }
    body, rewritten := rewriteRequestBody(req.Body, req.SourceFormat)
    // ... same as before
}

// cloakToolNames walks the parsed JSON and renames tool names
func cloakToolNames(body map[string]any, cloakTable map[string]string, sourceFormat string) bool {
    changed := false

    // Cloak tools[] array
    if toolsRaw, ok := body["tools"].([]any); ok {
        for _, tRaw := range toolsRaw {
            tMap, ok := tRaw.(map[string]any)
            if !ok { continue }
            if sourceFormat == "openai" {
                fn, ok := tMap["function"].(map[string]any)
                if !ok { continue }
                if name, ok := fn["name"].(string); ok {
                    if target, exists := cloakTable[name]; exists {
                        fn["name"] = target
                        changed = true
                    }
                }
            } else if sourceFormat == "anthropic" {
                if name, ok := tMap["name"].(string); ok {
                    if target, exists := cloakTable[name]; exists {
                        tMap["name"] = target
                        changed = true
                    }
                }
            }
        }
    }

    // Cloak tool refs in messages[]
    if msgsRaw, ok := body["messages"].([]any); ok {
        for _, mRaw := range msgsRaw {
            msg, ok := mRaw.(map[string]any)
            if !ok { continue }

            if sourceFormat == "openai" {
                // tool_calls[].function.name
                if calls, ok := msg["tool_calls"].([]any); ok {
                    for _, cRaw := range calls {
                        call, ok := cRaw.(map[string]any)
                        if !ok { continue }
                        fn, ok := call["function"].(map[string]any)
                        if !ok { continue }
                        if name, ok := fn["name"].(string); ok {
                            if target, exists := cloakTable[name]; exists {
                                fn["name"] = target
                                changed = true
                            }
                        }
                    }
                }
                // tool result message: msg["name"]
                if msg["role"] == "tool" {
                    if name, ok := msg["name"].(string); ok {
                        if target, exists := cloakTable[name]; exists {
                            msg["name"] = target
                            changed = true
                        }
                    }
                }
            } else if sourceFormat == "anthropic" {
                // content blocks with type == "tool_use"
                if contents, ok := msg["content"].([]any); ok {
                    for _, cntRaw := range contents {
                        cnt, ok := cntRaw.(map[string]any)
                        if !ok { continue }
                        if cnt["type"] == "tool_use" {
                            if name, ok := cnt["name"].(string); ok {
                                if target, exists := cloakTable[name]; exists {
                                    cnt["name"] = target
                                    changed = true
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    // Cloak tool_choice (handle both string and object shapes)
    if tc, ok := body["tool_choice"].(map[string]any); ok {
        if sourceFormat == "openai" {
            // {type: "function", function: {name: "..."}}
            if fn, ok := tc["function"].(map[string]any); ok {
                if name, ok := fn["name"].(string); ok {
                    if target, exists := cloakTable[name]; exists {
                        fn["name"] = target
                        changed = true
                    }
                }
            }
        } else if sourceFormat == "anthropic" {
            // {type: "tool", name: "..."}
            if name, ok := tc["name"].(string); ok {
                if target, exists := cloakTable[name]; exists {
                    tc["name"] = target
                    changed = true
                }
            }
        }
    }
    // If tool_choice is a string ("auto", "required", "none") — skip safely

    return changed
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run "TestRewriteRequestBody|TestRewriteRequestIgnoresKeywordsOutsideSystem" -v`
Expected: PASS

- [ ] **Step 5: Run all existing tests to check for regressions**

Run: `go test -v -count=1 ./...`
Expected: PASS (update any broken callers of `rewriteRequestBody` to pass `sourceFormat`)

- [ ] **Step 6: Commit**

```bash
git add main.go filter_test.go
git commit -m "feat: cloak tool names in requests with expanded brand replace scope"
```

### Task 5: Response and Stream Chunk Interception (Structured Uncloak)

**Files:**
- Modify: `main.go`
- Modify: `plugin_test.go`

**Interfaces:**
- Consumes: `handlePluginCall`, `RequestBody` context, structured JSON parsing
- Produces: `handleResponseIntercept`, `handleStreamChunkIntercept`

**Critical design: Structured JSON uncloak (NOT raw string replace)**

Response uncloak targets only tool-name-bearing fields to avoid false positives:
- **Non-streaming (response.intercept_after):** Parse JSON body → replace `tool_calls[].function.name` (OpenAI) / content blocks `type: "tool_use"` `.name` (Anthropic) → re-serialize
- **Streaming (response.intercept_stream_chunk):** Parse each SSE `data:` JSON line → replace only in tool-name fields → re-serialize. Non-JSON lines pass through untouched.

`request.intercept_after` stub: Keep as no-op — tool cloaking happens in `request.intercept_before`.

- [ ] **Step 1: Write the failing test**

```go
// Add to plugin_test.go

func TestResponseInterceptReversesClaudeCodeCloak(t *testing.T) {
    // Build a request body with Claude Code tools
    reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}}],"messages":[]}`
    // Build a response body with cloaked tool call
    respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

    request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
    raw, code := handlePluginCall("response.intercept_after", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }

    // Parse response → verify tool_calls[].function.name == "bash" (uncloaked)
    var envelope struct {
        OK     bool `json:"ok"`
        Result struct {
            Body string `json:"Body"`
        } `json:"result"`
    }
    mustUnmarshalJSON(t, raw, &envelope)
    // Decode Body, parse JSON, assert "bash" not "run_command"
}

func TestResponseInterceptReversesCodexCloak(t *testing.T) {
    reqBody := `{"tools":[{"type":"function","function":{"name":"shell_command"}}],"messages":[]}`
    respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

    request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
    raw, code := handlePluginCall("response.intercept_after", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }
    // Assert "shell_command" not "run_command"
}

func TestResponseInterceptDoesNotCorruptProse(t *testing.T) {
    // Verify that "run_command" appearing in assistant text is NOT replaced
    reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}}],"messages":[]}`
    respBody := `{"choices":[{"message":{"content":"You can use run_command to execute..."}}]}`

    request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
    raw, code := handlePluginCall("response.intercept_after", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }
    // Assert content still contains "run_command" — NOT replaced to "bash"
    // This is the key test proving structured replace works
}

func TestStreamChunkInterceptReversesCloak(t *testing.T) {
    reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}}],"messages":[]}`
    chunkBody := `data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}`

    request := streamChunkInterceptRequestJSON(t, reqBody, chunkBody, "openai")
    raw, code := handlePluginCall("response.intercept_stream_chunk", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }
    // Assert chunk contains "bash" not "run_command" in tool_calls field
}

func TestResponseInterceptPassesThroughAntigravity(t *testing.T) {
    reqBody := `{"tools":[{"type":"function","function":{"name":"ask_permission"}}],"messages":[]}`
    respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

    request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
    raw, code := handlePluginCall("response.intercept_after", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }
    // Assert empty Body (passthrough) — Antigravity detected, no uncloak needed
}

func TestResponseInterceptAnthropicFormat(t *testing.T) {
    reqBody := `{"tools":[{"name":"bash"}],"messages":[]}`
    respBody := `{"content":[{"type":"tool_use","id":"tu1","name":"run_command","input":{}}]}`

    request := responseInterceptRequestJSON(t, reqBody, respBody, "anthropic")
    raw, code := handlePluginCall("response.intercept_after", request)
    if code != 0 { t.Fatalf("code = %d; body=%s", code, raw) }
    // Assert content[0].name == "bash" (uncloaked)
}

// Helper
func responseInterceptRequestJSON(t *testing.T, reqBody, respBody, sourceFormat string) []byte {
    t.Helper()
    raw, err := json.Marshal(map[string]any{
        "SourceFormat": sourceFormat,
        "RequestBody":  []byte(reqBody),
        "Body":         []byte(respBody),
    })
    if err != nil { t.Fatalf("marshal: %v", err) }
    return raw
}

func streamChunkInterceptRequestJSON(t *testing.T, reqBody, chunkBody, sourceFormat string) []byte {
    t.Helper()
    raw, err := json.Marshal(map[string]any{
        "SourceFormat": sourceFormat,
        "RequestBody":  []byte(reqBody),
        "Body":         []byte(chunkBody),
    })
    if err != nil { t.Fatalf("marshal: %v", err) }
    return raw
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run "TestResponseIntercept|TestStreamChunkIntercept" -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// Add new cases to handlePluginCall:
case pluginabi.MethodResponseInterceptAfter:
    return handleResponseIntercept(request), 0
case pluginabi.MethodResponseInterceptStreamChunk:
    return handleStreamChunkIntercept(request), 0

// handleResponseIntercept — structured JSON uncloak for non-streaming
func handleResponseIntercept(request []byte) []byte {
    var req pluginapi.ResponseInterceptRequest
    if err := json.Unmarshal(request, &req); err != nil {
        return mustErrorEnvelope("invalid_request", err.Error())
    }

    uncloakTable := buildUncloakTable(req.RequestBody, req.SourceFormat)
    if uncloakTable == nil {
        return mustEnvelope(pluginapi.ResponseInterceptResponse{})
    }

    modified, changed := uncloakResponseBody(req.Body, uncloakTable, req.SourceFormat)
    if !changed {
        return mustEnvelope(pluginapi.ResponseInterceptResponse{})
    }
    return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
}

// handleStreamChunkIntercept — structured JSON uncloak for streaming
func handleStreamChunkIntercept(request []byte) []byte {
    var req pluginapi.StreamChunkInterceptRequest
    if err := json.Unmarshal(request, &req); err != nil {
        return mustErrorEnvelope("invalid_request", err.Error())
    }

    uncloakTable := buildUncloakTable(req.RequestBody, req.SourceFormat)
    if uncloakTable == nil {
        return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
    }

    modified, changed := uncloakStreamChunk(req.Body, uncloakTable, req.SourceFormat)
    if !changed {
        return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
    }
    return mustEnvelope(pluginapi.StreamChunkInterceptResponse{Body: modified})
}

// buildUncloakTable extracts client from RequestBody and returns the uncloak table
func buildUncloakTable(requestBody []byte, sourceFormat string) map[string]string {
    var reqRoot map[string]any
    if err := json.Unmarshal(requestBody, &reqRoot); err != nil {
        return nil
    }
    toolNames := extractToolNames(reqRoot, sourceFormat)
    client := detectClient(toolNames)
    if client == "" || client == "antigravity" {
        return nil
    }
    return effectiveUncloakTable(client)
}

// uncloakResponseBody parses JSON and replaces tool names in tool-name-bearing fields only
func uncloakResponseBody(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
    var root any
    if err := json.Unmarshal(body, &root); err != nil {
        return nil, false
    }

    changed := false
    // OpenAI: choices[].message.tool_calls[].function.name
    // Anthropic: content[].name where type == "tool_use"
    // Walk and replace only in these fields
    // ... (structured walk implementation)

    if !changed { return nil, false }
    raw, err := json.Marshal(root)
    if err != nil { return nil, false }
    return raw, true
}

// uncloakStreamChunk handles SSE stream chunks
func uncloakStreamChunk(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
    // For each line in body:
    //   If starts with "data: " → extract JSON, parse, uncloak tool-name fields, re-serialize
    //   If not JSON or not data line → pass through unchanged
    // ... (line-by-line processing)
}
```

> [!NOTE]
> **Per-chunk re-parse tradeoff:** Each stream chunk re-parses `RequestBody` to detect client. This is intentional simplicity over statefulness. For requests with very large tool arrays AND high chunk rates, this may become measurable — monitor in production. A future optimization could cache by request hash.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run "TestResponseIntercept|TestStreamChunkIntercept" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test -v -count=1 ./...`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add main.go plugin_test.go
git commit -m "feat: structured uncloak for responses and stream chunks"
```

---

## Post-Implementation Checklist

- [ ] Update README.md to reflect new capabilities and brand replace scope expansion
- [ ] Update `TestRewriteRequestIgnoresKeywordsOutsideSystem` to match new scope
- [ ] Verify `request.intercept_after` stub still returns empty response (no change needed)
- [ ] Run `go vet ./...` and `go test -race -v -count=1 ./...`
