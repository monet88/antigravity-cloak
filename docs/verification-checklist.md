# Oh My Pi ⟷ Antigravity Two-Way Verification Checklist

This document details the two-way verification protocol to validate bidirectional tool cloaking, brand rewriting, virtual device routing, and MCP pass-through between **Oh My Pi** (client) and **Antigravity** (backend via CLIProxyAPI).

---

## 1. Architecture Overview

```
[ Oh My Pi Client ]  --(1. Request: 'bash', 'read'...)-->  [ CLIProxyAPI + antigravity-cloak ]
                                                                     │
                                                      (Cloak: 'run_command', 'view_file')
                                                                     ▼
                                                             [ Antigravity Backend ]
                                                                     │
                                                       (Model outputs 'run_command')
                                                                     ▼
[ Oh My Pi Client ]  <--(3. Response: 'bash' restored)<--   [ CLIProxyAPI + antigravity-cloak ]
                                                              (Uncloak: restores original name)
```

1. **Request Phase (Client $\to$ Backend)**:
   - Client sends native tool names (`read`, `bash`, `edit`, ...).
   - `antigravity-cloak` detects `oh_my_pi` and rewrites tool definitions, `tool_choice`, and history `tool_calls` to Antigravity native names (`view_file`, `run_command`, `replace_file_content`, ...).
   - Brand tokens (`Oh My Pi`, `oh-my-pi`, `omp`) in `system` prompts and messages are rewritten to `Antigravity`.
2. **Response / SSE Stream Phase (Backend $\to$ Client)**:
   - Model backend generates Antigravity native tool calls (`view_file`, `run_command`, ...).
   - `antigravity-cloak` interceptor buffers SSE chunks (`\n\n` boundaries) and uncloaks tool names back to client originals (`read`, `bash`, ...).
   - Oh My Pi receives its native tool names and executes them without error.

---

## 2. Core Tool Verification Checklist (12 Tools)

| # | Oh My Pi Tool | Antigravity Tool | Sample Test Prompt | Expected Client Action | Expected Backend State |
| :-: | :--- | :--- | :--- | :--- | :--- |
| **1** | `read` | `view_file` | `"Read the first 10 lines of go.mod"` | Oh My Pi invokes `read` and shows file content | Backend sees `view_file` in request/response |
| **2** | `write` | `write_to_file` | `"Create test_verify.txt with content 'hello'"` | Oh My Pi invokes `write` to create file | Backend sees `write_to_file` in request/response |
| **3** | `edit` | `replace_file_content` | `"In test_verify.txt, change 'hello' to 'world'"` | Oh My Pi invokes `edit` with line patch | Backend sees `replace_file_content` |
| **4** | `bash` | `run_command` | `"Run shell command 'go version'"` | Oh My Pi invokes `bash` and prints output | Backend sees `run_command` in request/response |
| **5** | `grep` | `grep_search` | `"Search for 'defaultRewriteMappings' in main.go"` | Oh My Pi invokes `grep` with regex | Backend sees `grep_search` |
| **6** | `glob` | `list_dir` | `"Find all .go files in this repo"` | Oh My Pi invokes `glob` and lists paths | Backend sees `list_dir` |
| **7** | `task` | `invoke_subagent` | `"Spawn a scout subagent to check git status"` | Oh My Pi invokes `task` with subagent spec | Backend sees `invoke_subagent` |
| **8** | `ask` | `ask_question` | `"Ask me a question with 2 choices: A or B"` | Oh My Pi invokes `ask` interactive UI | Backend sees `ask_question` |
| **9** | `todo` | `manage_task` | `"Initialize a 2-step verification todo list"` | Oh My Pi invokes `todo` managing phase/items | Backend sees `manage_task` |
| **10** | `hub` | `send_message` | `"Check running background jobs via hub"` | Oh My Pi invokes `hub` for process status | Backend sees `send_message` |
| **11** | `web_search` | `search_web` | `"Search latest release notes for Go 1.26"` | Oh My Pi invokes `web_search` | Backend sees `search_web` |
| **12** | `eval` | `execute_code` | `"Evaluate Python code printing 1+1"` | Oh My Pi invokes `eval` persistent kernel | Backend sees `execute_code` |

---

## 3. Virtual Device Tools (`xd://`)

Oh My Pi routes advanced capabilities through the **Device Bus** (`xd://<device>`) by calling the `write` tool:

- **`lsp`** (Language Server Protocol): Navigation, diagnostics, symbol queries
- **`ast_grep` / `ast_edit`**: AST-aware syntax search and codemods
- **`browser`**: Headless Chromium Puppeteer automation
- **`debug`**: Debug Adapter Protocol (DAP) stepping and inspection
- **`computer`**: Desktop automation
- **`checkpoint` / `rewind`**: Context window pruning

**Verification**: Because `write` is cloaked to `write_to_file` and restored transparently, all `xd://` virtual devices operate seamlessly.

---

## 4. MCP Tools Pass-Through (`mcp__*`)

Model Context Protocol (MCP) tools adhere to the following rules:

1. **Prefix Convention**: Every MCP tool starts with `mcp__<server>_<tool>` (e.g. `mcp__gitnexus_query`, `mcp__fastctx_grep`, `mcp__context_get_library_docs`).
2. **Passthrough Guarantee**: `antigravity-cloak` leaves `mcp__*` tools completely untouched in both request and response.
3. **Backend Support**: Antigravity natively accepts dynamic custom function schemas defined in `tools[]`.

---

## 5. Step-by-Step Verification Procedure

### Step 1: Build & Deploy Plugin

Compile the shared library for your host environment:

```powershell
# Linux amd64 (for Docker container):
docker run --rm -v F:\CodeBase\antigravity-cloak:/src -w /src golang:1.26 sh -c "mkdir -p dist && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -ldflags '-s -w' -o dist/antigravity-cloak.so . && rm -f dist/antigravity-cloak.h"

# Deploy to container:
cd F:\cliproxy
docker compose stop
Copy-Item F:\CodeBase\antigravity-cloak\dist\antigravity-cloak.so F:\cliproxy\plugins\linux\amd64\antigravity-cloak-v0.3.0.so -Force
docker compose start
```

### Step 2: Enable Debug Logging

Set `CPA_FILTER_DEBUG=1` in `docker-compose.yml` (requires container recreate: `docker compose up -d`).

### Step 3: Execute & Cross-Check Logs

1. Send a request from **Oh My Pi** (e.g. *"Read go.mod"*).
2. Inspect `logs/cpa-filter-debug.log`:
   - **Client Detection**: `buildUncloakTable: client=oh_my_pi`
   - **Request Cloak**: `tools[i].name` rewritten from `read` $\to$ `view_file`
   - **System Brand Rewrite**: `Oh My Pi` $\to$ `Antigravity`
   - **Response Uncloak**: `view_file` restored $\to$ `read`
3. Verify that Oh My Pi completes the action without schema or unknown tool errors.
