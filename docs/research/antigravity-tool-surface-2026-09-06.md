# Antigravity Native Tool Surface Reference

## Overview

This document defines the canonical, ground-truth native tool surface of the live Google Antigravity runtime environment (AGY CLI harness). It serves as the primary technical reference for all native tools exposed to the agent, detailing their wire schemas, parameters, execution semantics, and operational categories.

Client-specific cloaking mappings (such as Oh My Pi, Claude Code, or Codex translation tables) are excluded from this specification; those reside separately in **[CONTEXT.md](../../CONTEXT.md)**.

---

## Architecture & Surface Distinctions

### Gemini API Managed Agents vs. AGY CLI Harness

1. **Gemini API Managed Agent Surface**: Google's managed agent documentation describes high-level capability abstractions (`code_execution`, `google_search`, `url_context`, custom `function`, and `mcp_server`). That namespace does not match the harness-level tool namespace.
2. **AGY CLI Agent Runtime**: The live AGY CLI environment operates with a concrete set of **20 native tools**. These tools provide deterministic primitives for file manipulation, system execution, background task supervision, subagent orchestration, search, user interaction, and MCP protocol integration.
3. **Obsolete & Non-Existent Tool Names**: Previous specifications and historical documentation referenced tools that **do not exist** in the live Antigravity runtime:
   - `execute_code` (subsumed by `run_command`)
   - `wait`, `cancel` (subsumed by `manage_task` / `schedule`)
   - `list` (subsumed by `list_dir` / `find_by_name`)
   - `multi_replace_file_content` (subsumed by `replace_file_content`)
   - `update_plan` (handled via task artifact protocol)
   - `list_permissions` (not exposed)
   - `create_goal`, `update_goal` (handled via UI/slash commands)

---

## Universal Envelope Fields

Every tool call in the Antigravity runtime requires two standard metadata envelope parameters:

- **`toolAction`** (`string`, required): A brief 2–5 word summary of the immediate action (e.g., `"Viewing file"`, `"Running command"`, `"Editing file"`).
- **`toolSummary`** (`string`, required): A brief 2–5 word noun phrase describing the subject or context (e.g., `"File view"`, `"Command execution"`, `"File edit"`).

---

## Complete 20 Native Tools Inventory

### 1. File & Filesystem Operations (5 tools)

#### `view_file`
- **Description**: View the contents of a file from the local filesystem. Supports text files and binary files (image, pdf, video, audio). Slices text files with 1-indexed line ranges (max 800 lines or 46,080 bytes per call).
- **Parameters**:
  - `AbsolutePath` (`string`, required): Absolute filesystem path to the target file.
  - `StartLine` (`integer`, optional): Starting line number (1-indexed, inclusive).
  - `EndLine` (`integer`, optional): Ending line number (1-indexed, inclusive).
  - `ContentOffset` (`integer`, optional): Byte offset for reading truncated content.

#### `write_to_file`
- **Description**: Create new files or overwrite existing files. When creating artifact documents in the artifact directory, structured `ArtifactMetadata` is required.
- **Parameters**:
  - `TargetFile` (`string`, required): Absolute filesystem path of the file to create or overwrite.
  - `Overwrite` (`boolean`, required): Must be `true` to replace existing file contents.
  - `CodeContent` (`string`, required): Complete code or text content to write.
  - `Description` (`string`, required): User-facing explanation of the rationale/changes.
  - `ArtifactMetadata` (`object`, optional): Required only when creating an artifact file.
    - `Summary` (`string`, required): Detailed multi-line summary of the artifact.
    - `UserFacing` (`boolean`, required): `true` if visible to user; `false` for internal scratch files.
    - `RequestFeedback` (`boolean`, required): `true` to request user feedback/approval.

#### `replace_file_content`
- **Description**: Make a single contiguous block replacement in an existing file. Requires exact character-for-character matching including whitespace and line endings.
- **Parameters**:
  - `TargetFile` (`string`, required): Absolute filesystem path of the file to modify.
  - `Instruction` (`string`, required): Description of the modification being performed.
  - `Description` (`string`, required): User-facing explanation of the change.
  - `AllowMultiple` (`boolean`, required): Whether to allow replacing multiple identical instances.
  - `TargetContent` (`string`, required): Exact string chunk to be replaced.
  - `ReplacementContent` (`string`, required): Exact replacement chunk.
  - `StartLine` (`integer`, required): 1-indexed starting line of the search range.
  - `EndLine` (`integer`, required): 1-indexed ending line of the search range.
  - `TargetLintErrorIds` (`array[string]`, optional): IDs of lint errors targeted by this edit.

#### `find_by_name`
- **Description**: Fast search for files and subdirectories using `fd` glob pattern matching. Respects gitignore by default and caps results at 50 matches.
- **Parameters**:
  - `SearchDirectory` (`string`, required): Absolute path of the directory to search within.
  - `Pattern` (`string`, required): Glob search pattern.
  - `Excludes` (`array[string]`, optional): Glob patterns to exclude from search.
  - `Extensions` (`array[string]`, optional): File extensions to include (without leading dot).
  - `FullPath` (`boolean`, optional): Match against full absolute path rather than filename only.
  - `MaxDepth` (`integer`, optional): Maximum directory recursion depth.
  - `Type` (`string`, optional, enum: `["file", "directory", "any"]`): Filter by filesystem entry type.

#### `list_dir`
- **Description**: List the immediate children of a specified directory (relative paths, entry type, size in bytes, and recursive child count).
- **Parameters**:
  - `DirectoryPath` (`string`, required): Absolute path of the directory to inspect.

---

### 2. Execution & Process Management (3 tools)

#### `run_command`
- **Description**: Execute a shell command directly on the host system (PowerShell on Windows, Bash on POSIX). If execution exceeds `WaitMsBeforeAsync`, the process automatically transitions to a managed background task.
- **Parameters**:
  - `CommandLine` (`string`, required): Exact command line string to execute.
  - `Cwd` (`string`, required): Working directory for process execution.
  - `WaitMsBeforeAsync` (`integer`, required): Milliseconds to wait synchronously before backgrounding (max 10,000 ms).

#### `manage_task`
- **Description**: Manage background operating system processes, long-running commands, and scheduled timers/crons.
- **Parameters**:
  - `Action` (`string`, required, enum: `["list", "kill", "status", "send_input"]`): Operation to perform.
  - `TaskId` (`string`, optional): Task identifier (required for `kill`, `status`, `send_input`).
  - `Input` (`string`, optional): Input text to stream to process stdin (required for `send_input`).

#### `schedule`
- **Description**: Schedule an asynchronous one-shot wake-up timer or recurring cron job. Returns immediately and wakes the agent upon expiration or trigger.
- **Parameters**:
  - `Prompt` (`string`, required): Notification message content sent to agent when triggered.
  - `DurationSeconds` (`integer`, optional): Duration in seconds for one-shot timers (mutually exclusive with `CronExpression`).
  - `CronExpression` (`string`, optional): Standard 5-field cron expression for recurring tasks (mutually exclusive with `DurationSeconds`).
  - `MaxIterations` (`integer`, optional): Maximum trigger count for recurring cron schedules.
  - `TimerCondition` (`string`, optional, default `"never"`): Early termination condition (`"never"`, `"any"`, or specific sender/task ID).

---

### 3. Search & Content Retrieval (3 tools)

#### `grep_search`
- **Description**: Fast pattern search within files using `ripgrep`. Supports regular expressions, case-insensitive search, and glob filters. Results capped at 50 matches.
- **Parameters**:
  - `SearchPath` (`string`, required): Absolute path to directory or file to search.
  - `Query` (`string`, required): Search string or regular expression.
  - `CaseInsensitive` (`boolean`, optional): Enable case-insensitive matching.
  - `IsRegex` (`boolean`, optional): Treat query as a regular expression.
  - `MatchPerLine` (`boolean`, optional): Return line numbers and content snippets instead of filenames only.
  - `Includes` (`array[string]`, optional): Glob patterns to filter files within `SearchPath`.

#### `search_web`
- **Description**: Perform external web searches with summarized snippets and URL citations.
- **Parameters**:
  - `query` (`string`, required): Search query string.
  - `domain` (`string`, optional): Domain hint to prioritize.

#### `read_url_content`
- **Description**: Fetch web pages via HTTP request and convert HTML content into structured Markdown without browser overhead.
- **Parameters**:
  - `Url` (`string`, required): Complete HTTP/HTTPS URL to fetch.

---

### 4. Subagents & Multi-Agent Coordination (4 tools)

#### `invoke_subagent`
- **Description**: Concurrently launch one or more background subagents by type name with custom prompts and role specifications.
- **Parameters**:
  - `Subagents` (`array[object]`, required): List of subagent definitions:
    - `TypeName` (`string`, required): Registered subagent type name (e.g., `"self"`, `"research"`, or custom).
    - `Role` (`string`, required): 2–5 word job title / role description.
    - `Prompt` (`string`, required): Clear, actionable task prompt.
    - `Model` (`string`, optional, default `"inherit"`, enum: `["inherit", "flash_lite", "flash", "pro"]`): Model tier.
    - `Workspace` (`string`, optional, default `"inherit"`, enum: `["inherit", "branch", "share"]`): Workspace isolation mode.

#### `define_subagent`
- **Description**: Dynamically define a new specialized subagent type available for subsequent invocations within the session.
- **Parameters**:
  - `name` (`string`, required): Unique identifier for the subagent type.
  - `description` (`string`, required): Human-readable purpose description.
  - `system_prompt` (`string`, required): Complete system instructions for the subagent.
  - `enable_write_tools` (`boolean`, optional): Grant file modification and command execution capabilities.
  - `enable_subagent_tools` (`boolean`, optional): Grant nested subagent definition and invocation capabilities.
  - `enable_mcp_tools` (`boolean`, optional): Grant MCP tool execution capabilities.

#### `manage_subagents`
- **Description**: Query live state or terminate running subagents and their descendants.
- **Parameters**:
  - `Action` (`string`, required, enum: `["list", "kill", "kill_all"]`): Management action.
  - `ConversationIds` (`array[string]`, optional): IDs of subagents to terminate (required for `kill`).

#### `send_message`
- **Description**: Send a message to an active subagent or peer conversation channel (never used for end-user communication).
- **Parameters**:
  - `Recipient` (`string`, required): Target agent conversation ID.
  - `Message` (`string`, required): Message payload content.

---

### 5. Interaction, Media & MCP Infrastructure (5 tools)

#### `ask_question`
- **Description**: Render an interactive multiple-choice prompt modal to clarify underspecified requirements or solicit user preferences. Blocks execution until user answers.
- **Parameters**:
  - `questions` (`array[object]`, required): Structured list of questions:
    - `question` (`string`, required): Question text.
    - `options` (`array[string]`, required): List of selectable option strings.
    - `is_multi_select` (`boolean`, optional): Allow selecting multiple choices.

#### `generate_image`
- **Description**: Generate or edit user interface mockups, application assets, or graphics from text prompts and optional reference images.
- **Parameters**:
  - `Prompt` (`string`, required): Generation prompt or edit instructions.
  - `ImageName` (`string`, required): Snake_case filename identifier (max 3 words).
  - `AspectRatio` (`string`, optional, default `"1:1"`, enum: `["1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9"]`).
  - `ImagePaths` (`array[string]`, optional): Up to 3 absolute paths to reference images.

#### `call_mcp_tool`
- **Description**: Execute a tool exposed by a registered Model Context Protocol (MCP) server.
- **Parameters**:
  - `ServerName` (`string`, required): Registered MCP server identifier.
  - `ToolName` (`string`, required): Target tool name on that server.
  - `Arguments` (`object`, required): Tool arguments matching the server's published JSON schema.

#### `list_resources`
- **Description**: List resources available on a connected MCP server.
- **Parameters**:
  - `ServerName` (`string`, required): Registered MCP server identifier.

#### `read_resource`
- **Description**: Fetch the raw content of a specific resource URI from an MCP server.
- **Parameters**:
  - `ServerName` (`string`, required): Registered MCP server identifier.
  - `Uri` (`string`, required): Unique resource URI to read.

---

## State & Protocol Conventions

### Task Tracking Protocol vs. Process Management
- **No Built-in Todo Tool**: Antigravity has no native `todo` tool. Checklist and task management is implemented via the **Task Artifact Protocol**:
  - Created in `<appDataDir>\brain\<conversation-id>\task.md` using `write_to_file` with `ArtifactMetadata` (`UserFacing: true`, `Summary: "..."`, `RequestFeedback: false`).
  - Updated incrementally via `replace_file_content` (`- [ ]` $\to$ `- [x]`).
- **`manage_task` Scope**: Strictly dedicated to operating system background processes, command execution timeouts, and scheduled timers/crons.

---

## Primary Verification Sources

- **Live AGY CLI Direct Introspection**: Verified against live runtime declarations on 2026-09-12.
- **Google Antigravity Agent Specifications**: System prompt contract and declared schemas.
