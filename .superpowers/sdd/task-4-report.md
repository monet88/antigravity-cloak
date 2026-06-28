# Task 4 Report: Cloak Request Payload

## Implementation Summary

In Task 4, we implemented tool name cloaking in request payloads for both OpenAI and Anthropic formats. 

1. **`rewriteRequestBody` Signature Change**:
   Updated the signature from `func rewriteRequestBody(body []byte) ([]byte, bool)` to `func rewriteRequestBody(body []byte, sourceFormat string) ([]byte, bool)`.
2. **`handleRequestInterceptBefore` Interception**:
   Modified `handleRequestInterceptBefore` in `main.go` to pass `req.SourceFormat` to `rewriteRequestBody`.
3. **Cumulative Modification Tracking**:
   Aggregated changes across all stages into a unified `changed` flag. The modifications handled are:
   - System fields brand replace (existing logic)
   - Tool name cloaking
   - Tool description brand replace
   - System message brand replace
4. **Tool Cloaking**:
   Implemented `cloakToolNames` to look up active mappings (from default + config overrides) and rename tools in:
   - `tools[]` array (handling OpenAI `function.name` vs. Anthropic `name` shapes).
   - `messages[]` history (handling OpenAI `tool_calls[].function.name` & `role: "tool"` name vs. Anthropic content blocks with `type: "tool_use"`).
   - `tool_choice` parameter (handling string constants as well as OpenAI `function.name` and Anthropic `name` object shapes).
5. **Tool Description Brand Replace**:
   Implemented `rewriteToolDescriptions` to perform case-insensitive brand text replacements on descriptions for all tools in `tools[]` array.
6. **System Message Brand Replace**:
   Implemented `rewriteSystemMessages` to recursively rewrite `content` fields of messages within the request history where `role == "system"`.

---

## TDD Evidence

### RED Test Stage
**Command:**
```powershell
go test -run "TestRewriteRequestBody|TestRewriteRequestIgnoresKeywordsOutsideSystem" -v
```

**Output:**
```
=== RUN   TestRewriteRequestIgnoresKeywordsOutsideSystem
--- PASS: TestRewriteRequestIgnoresKeywordsOutsideSystem (0.00s)
=== RUN   TestRewriteRequestBodyCloaksClaudeCodeTools
    filter_test.go:118: tools[0] name = "bash", want run_command
    filter_test.go:122: tools[0] description = "Run Claude Code shell commands", want 'Run Antigravity shell commands'
    filter_test.go:128: tools[1] name = "read", want view_file
    filter_test.go:134: tool_choice name = "bash", want run_command
--- FAIL: TestRewriteRequestBodyCloaksClaudeCodeTools (0.00s)
=== RUN   TestRewriteRequestBodyCloaksCodexTools
    filter_test.go:160: tools[0] name = "shell_command", want run_command
    filter_test.go:163: tools[0] description = "Execute Codex shell", want 'Execute Antigravity shell'
    filter_test.go:168: tools[1] name = "apply_patch", want multi_replace_file_content
--- FAIL: TestRewriteRequestBodyCloaksCodexTools (0.00s)
=== RUN   TestRewriteRequestBodyCloaksToolRefsInMessageHistory
    filter_test.go:181: want rewritten
--- FAIL: TestRewriteRequestBodyCloaksToolRefsInMessageHistory (0.00s)
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_string_(skip_safely)
    filter_test.go:222: want rewritten
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_object_with_function.name
    filter_test.go:222: want rewritten
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/anthropic_tool_choice_as_object_with_name
    filter_test.go:222: want rewritten
--- FAIL: TestRewriteRequestBodyHandlesToolChoiceShapes (0.00s)
    --- FAIL: TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_string_(skip_safely) (0.00s)
    --- FAIL: TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_object_with_function.name (0.00s)
    --- FAIL: TestRewriteRequestBodyHandlesToolChoiceShapes/anthropic_tool_choice_as_object_with_name (0.00s)
=== RUN   TestRewriteRequestBodySkipsAntigravityTools
--- PASS: TestRewriteRequestBodySkipsAntigravityTools (0.00s)
=== RUN   TestRewriteRequestBodySkipsUnknownTools
--- PASS: TestRewriteRequestBodySkipsUnknownTools (0.00s)
=== RUN   TestRewriteRequestBodyAppliesBrandReplaceToToolDescription
    filter_test.go:271: want rewritten
--- FAIL: TestRewriteRequestBodyAppliesBrandReplaceToToolDescription (0.00s)
=== RUN   TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages
    filter_test.go:291: want rewritten
--- FAIL: TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages (0.00s)
=== RUN   TestRewriteRequestBodyCloaksAnthropicFormat
    filter_test.go:327: tools[0] name = "bash", want run_command
    filter_test.go:330: tools[0] description = "Run Claude Code shell", want 'Run Antigravity shell'
    filter_test.go:335: tools[1] name = "read", want view_file
    filter_test.go:343: content name = "bash", want run_command
--- FAIL: TestRewriteRequestBodyCloaksAnthropicFormat (0.00s)
FAIL
exit status 1
FAIL	cpa-plugin-antigravity-coding-filter	0.015s
```

---

### GREEN Test Stage
**Command:**
```powershell
go test -run "TestRewriteRequestBody|TestRewriteRequestIgnoresKeywordsOutsideSystem" -v
```

**Output:**
```
=== RUN   TestRewriteRequestIgnoresKeywordsOutsideSystem
--- PASS: TestRewriteRequestIgnoresKeywordsOutsideSystem (0.00s)
=== RUN   TestRewriteRequestBodyCloaksClaudeCodeTools
--- PASS: TestRewriteRequestBodyCloaksClaudeCodeTools (0.00s)
=== RUN   TestRewriteRequestBodyCloaksCodexTools
--- PASS: TestRewriteRequestBodyCloaksCodexTools (0.00s)
=== RUN   TestRewriteRequestBodyCloaksToolRefsInMessageHistory
--- PASS: TestRewriteRequestBodyCloaksToolRefsInMessageHistory (0.00s)
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_string_(skip_safely)
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_object_with_function.name
=== RUN   TestRewriteRequestBodyHandlesToolChoiceShapes/anthropic_tool_choice_as_object_with_name
--- PASS: TestRewriteRequestBodyHandlesToolChoiceShapes (0.00s)
    --- PASS: TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_string_(skip_safely) (0.00s)
    --- PASS: TestRewriteRequestBodyHandlesToolChoiceShapes/tool_choice_as_object_with_function.name (0.00s)
    --- PASS: TestRewriteRequestBodyHandlesToolChoiceShapes/anthropic_tool_choice_as_object_with_name (0.00s)
=== RUN   TestRewriteRequestBodySkipsAntigravityTools
--- PASS: TestRewriteRequestBodySkipsAntigravityTools (0.00s)
=== RUN   TestRewriteRequestBodySkipsUnknownTools
--- PASS: TestRewriteRequestBodySkipsUnknownTools (0.00s)
=== RUN   TestRewriteRequestBodyAppliesBrandReplaceToToolDescription
--- PASS: TestRewriteRequestBodyAppliesBrandReplaceToToolDescription (0.00s)
=== RUN   TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages
--- PASS: TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages (0.00s)
=== RUN   TestRewriteRequestBodyCloaksAnthropicFormat
--- PASS: TestRewriteRequestBodyCloaksAnthropicFormat (0.00s)
PASS
ok  	cpa-plugin-antigravity-coding-filter	0.020s
```

---

## Files Changed

- [main.go](file:///F:/CodeBase/cpa-plugin-antigravity-coding-filter/main.go):
  - Changed signature of `rewriteRequestBody` to accept `sourceFormat`.
  - Updated `handleRequestInterceptBefore` to pass `req.SourceFormat`.
  - Added helper functions `effectiveCloakTable`, `cloakToolNames`, `rewriteToolDescriptions`, and `rewriteSystemMessages`.
- [filter_test.go](file:///F:/CodeBase/cpa-plugin-antigravity-coding-filter/filter_test.go):
  - Updated `TestRewriteRequestReplacesDefaultSystemKeywords`, `TestRewriteRequestIgnoresKeywordsOutsideSystem`, `TestRewriteRequestAllowsCleanInvalidAndStructuralBodies` to call `rewriteRequestBody` with `"openai"`.
  - Added new comprehensive test cases: `TestRewriteRequestBodyCloaksClaudeCodeTools`, `TestRewriteRequestBodyCloaksCodexTools`, `TestRewriteRequestBodyCloaksToolRefsInMessageHistory`, `TestRewriteRequestBodyHandlesToolChoiceShapes`, `TestRewriteRequestBodySkipsAntigravityTools`, `TestRewriteRequestBodySkipsUnknownTools`, `TestRewriteRequestBodyAppliesBrandReplaceToToolDescription`, `TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages`, and `TestRewriteRequestBodyCloaksAnthropicFormat`.
- [plugin_test.go](file:///F:/CodeBase/cpa-plugin-antigravity-coding-filter/plugin_test.go):
  - Updated callers of `rewriteRequestBody` to pass `"openai"` as the second argument.

---

## Self-Review Findings & Decisions

- **Client Detection Constraint**: Claude Code client detection requires either `askUserQuestion` or at least 3 tools from the signature trio (`bash`, `edit`, `read`) to avoid matching false positives. The test cases in `filter_test.go` were adjusted to ensure `edit` and `read` are included in the tools list when cloaking is expected to fire for Claude Code tools.
- **System Message Content Block Flexibility**: Standard OpenAI system messages have `role: "system"` and a string `content`. Anthropic generally separates systems from messages, but if system messages are nested inside `messages` arrays, we recursively scan system messages' `content` using `rewriteSystemValue` which handles string, map, and array block values, ensuring future-proof structural robustness.
