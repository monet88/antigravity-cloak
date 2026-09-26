# OMP Full Tool-Cloak Verification Checklist

Use this checklist to verify the installed plugin through the **real OMP client**.
The canonical mapping contract is [CONTEXT.md](../CONTEXT.md). Build, install,
profile setup, debug capture, cleanup, and rollback follow the
[local deployment runbook](verification-checklist.md). Complete its preparation
before running this matrix, and its cleanup after every controlled debug batch.

## Current evidence and acceptance rule

The recorded v0.5.1 run on 2026-09-12 proved tag sanitization and the
`bash -> run_command -> bash` execution/continuation path. The historical
2026-09-07 nine-tool run used a different plugin/OMP version. It does **not**
establish full nine-tool acceptance for the current binary.

Every result below starts **NOT RUN**. Record PASS, FAIL, BLOCKED, or NOT RUN
with evidence. A prompt requesting a tool, a declaration rewrite, an HTTP 200,
or a model claiming success is insufficient. Full canonical acceptance requires
all nine tools to execute successfully on the pinned binary. An unavailable
canonical tool is BLOCKED, not PASS. Unexposed extended tools may be N/A
only with a recorded inventory/configuration reason; any exposed or declared
extended tool must be cloaked to its assigned alias and restored downstream
rather than skipped as pass-through.

## 1. Prepare and pin the run

- [ ] Record source commit, worktree changes, plugin version, built and installed
  `.so` SHA256, gateway image/version, OMP version, profile, endpoint, and model.
- [ ] Follow the deployment runbook to discover mounts, build Linux/amd64 CGO
  `.so`, stop before replacement, recreate, and verify plugin registration/hash.
  If the intended binary is already installed and verified, record that fact.
- [ ] Use isolated `cloak-live` for local Docker acceptance. Confirm its endpoint
  and `X-Cloak-Client: oh_my_pi`; refresh its models if needed. Keep the default
  OMP profile unchanged and record a separate default-profile smoke result.
- [ ] Inspect actual outbound OMP tool declarations and schemas. Record which
  canonical and optional tools are exposed. Use the installed OMP help/config
  to enable missing tools; do not invent tool arguments or provider settings.
- [ ] Enable `CPA_FILTER_DEBUG` and gateway request logging as described in the
  runbook, then recreate. Run only a small batch before disabling and cleaning
  logs: debug captures full bodies and can overwhelm Windows bind-mount I/O.
- [ ] Keep evidence local under the runbook's `$EvidenceDir`; publish only
  redacted summaries. Record every retry and upstream status for each case.

In the same PowerShell session as the runbook, create a unique fixture directory.
All writes and edits in this matrix must target that directory.

```powershell
if (!(Test-Path -LiteralPath $EvidenceDir -PathType Container)) {
    throw 'Prepare EvidenceDir using the deployment runbook first'
}
$FixtureRoot = Join-Path $EvidenceDir ('omp-fixture-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $FixtureRoot -ErrorAction Stop | Out-Null
[IO.File]::WriteAllText((Join-Path $FixtureRoot 'sample.txt'), "alpha`nbeta`n")
[IO.File]::WriteAllText((Join-Path $FixtureRoot 'edit-me.txt'), "before`n")
```

## 2. Evidence required for every canonical tool

Correlate gateway request IDs, tool-call IDs, and OMP events across the entire
tool execution and its following continuation request:

1. Original `REQUEST BODY`: the native OMP tool is declared with its real schema.
2. Upstream `API REQUEST`: that declaration uses the expected cloaked name;
   the parameter schema and argument semantics remain compatible with OMP.
3. Upstream `API RESPONSE`: the model actually calls the cloaked tool name.
4. Client-facing `RESPONSE`: the same call ID has the native OMP tool name and
   intact arguments. Inspect parsed name fields: a call ID may legitimately
   contain a cloaked-name prefix.
5. OMP executes the native tool successfully (`tool_execution_end` or equivalent
   TUI/session evidence), produces the expected real result, and sends that
   result in a continuation that the model consumes successfully.

Inspect every upstream attempt, not just the last successful retry. Record
unknown-tool/schema errors, 429s, and plugin errors separately. A provider
failure without enough wire evidence leaves the affected case unresolved.

For non-interactive cases, this is a **single-case template**, initially set to
OMP-01. Change the case ID, allowed tools, and prompt using the table below.
Use a fresh session for each case; retain dependent tools such as `read` for
the edit case. If a model uses a substitute tool, the requested case remains
NOT RUN even if it reaches the desired file state.

```powershell
$CaseId = 'OMP-01'
$AllowedTools = 'read'
$CasePrompt = "Use read to read '$FixtureRoot/sample.txt'. Report its exact contents."
$CaseOutput = Join-Path $EvidenceDir ($CaseId + '-' + [guid]::NewGuid().ToString('N') + '.jsonl')
omp --profile cloak-live --model cpa/agy/gemini-3.8-flash --cwd $FixtureRoot --tools $AllowedTools --no-session --max-time 2m --mode json -p $CasePrompt *> $CaseOutput
if ($LASTEXITCODE) { Write-Warning "OMP exited with code $LASTEXITCODE; inspect $CaseOutput" }
```

Select a model actually listed by the profile. Follow the installed client's
permission flow for these fixture operations; a blocked approval is not a
cloak failure or a passed execution. Increase a bounded timeout for `task` or
search if necessary and record it.

## 3. Execute all nine canonical mappings

Substitute the absolute fixture path in the prompts. The universal evidence
chain above is mandatory for every row, in addition to its expected result.

| ID / initial status | Native -> upstream -> client | Suggested operation | Expected real result |
| --- | --- | --- | --- |
| OMP-01 / NOT RUN | `read -> view_file -> read` | Use read on `sample.txt`. | Exact `alpha` and `beta` lines returned. |
| OMP-02 / NOT RUN | `write -> write_to_file -> write` | Use write to create `written.txt` containing `OMP_WRITE_OK`. | File exists with that content; independently read it after the call. |
| OMP-03 / NOT RUN | `edit -> replace_file_content -> edit` | Read `edit-me.txt` to obtain fresh line anchors, then use edit to replace `before` with `after`. Expose `read,edit`. | Actual edit execution succeeds with OMP hashline arguments; file contains `after`. |
| OMP-04 / NOT RUN | `bash -> run_command -> bash` | Use bash exactly once to run `printf OMP_BASH_OK`. | Shell exits 0 and returns `OMP_BASH_OK`. |
| OMP-05 / NOT RUN | `grep -> grep_search -> grep` | Use grep to find `beta` in the fixture directory. | Match identifies `sample.txt` and its `beta` line. |
| OMP-06 / NOT RUN | `glob -> find_by_name -> glob` | Use glob to find `sample.txt` in the fixture directory. | Result includes the real fixture file. |
| OMP-07 / NOT RUN | `task -> invoke_subagent -> task` | Use task to delegate reading `sample.txt` to one child; return the child's result. | OMP actually starts and completes a child; parent receives `alpha` and `beta` and continues. |
| OMP-08 / NOT RUN | `ask -> ask_question -> ask` | Use ask to offer choices `alpha` and `beta`; report the user's choice. | Real question UI appears, user selects `beta`, tool result and continuation contain `beta`. |
| OMP-09 / NOT RUN | `web_search -> search_web -> web_search` | Use web_search to find the official Go release history page. | Real top-level search executes, returns a relevant source URL, and continuation uses the result. |

Special setup and checks:

- **task:** Use an available child/agent configuration from the installed OMP
  schema. Capture parent execution and child completion. If the child sends its
  own requests through the gateway, correlate those separately; parent success
  alone does not prove the child's routing/cloaking.
- **ask:** Run an interactive terminal, not the `-p --mode json` template. Start
  `omp --profile cloak-live --model cpa/agy/gemini-3.8-flash --cwd $FixtureRoot`
  in PowerShell, then enter the prompt. Retain gateway correlation and a redacted
  record of the displayed question, selected answer, and final continuation.
- **web_search:** Confirm it appears as a real top-level declaration. If provider
  configuration is required, back up and temporarily change only `cloak-live`,
  using an already configured provider, then restore it. Search via bash, a
  browser, or an MCP virtual device does not pass OMP-09.

## 4. Transport variants and extended alias tools

These are additional compatibility checks; the nine canonical rows alone do
not prove every virtual device or optional tool works.

| ID / initial status | Case | Required observation |
| --- | --- | --- |
| EXT-01 / NOT RUN | `read` directory | Real directory listing via `view_file -> read`. |
| EXT-02 / NOT RUN | `read` URL | Read a public text URL; returned content via `view_file -> read`. Record network failures separately. |
| EXT-03 / NOT RUN | `read` virtual device | Inspect a registered `xd://` device through `view_file -> read`; URI and device result survive unchanged. |
| EXT-04 / NOT RUN | `write` virtual device | Dispatch a benign operation using the device's actual schema via `write_to_file -> write`; verify real execution. |
| EXT-05 / NOT RUN | OMP MCP device | Exercise an available read-only `xd://mcp__<server>_<tool>` through native read/write; no conversion to `call_mcp_tool`, no URI/argument corruption. |
| EXT-06 / NOT RUN | Top-level `mcp__*` / optional tools | If exposed, declaration is cloaked upstream via shared `wp_*` or fallback `wp_ext_<hash>` alias and restored exactly on response/stream chunks. Record N/A if this client exposes MCP only through devices. |

For each **exposed** optional tool below, add an individual result row. Verify
its name is cloaked upstream to its assigned shared/fallback alias and restored
downstream, use a benign fixture operation supported by the actual schema, and
capture execution/continuation. Distinguish declaration-only verification from
executed-tool verification. Do not stop or modify unrelated agents/experiments
to manufacture coverage.

- [ ] `todo`, `hub`, `eval`.
- [ ] `vibe_spawn`, `vibe_send`, `vibe_wait`, `vibe_kill`, `vibe_list`.
- [ ] `init_experiment`, `run_experiment`, `log_experiment`, `update_notes`.

## 5. Routing and protocol checks

Record these separately from real-client tool acceptance. Use the linked specs
and existing harnesses for cases the OMP UI cannot construct. The
[supplemental HTTP matrix](../tests/test_issue28_live_matrix.py) uses simulated
tool results and therefore cannot replace section 3. The root Go suite is an
offline contract/lifecycle harness, not proof of a live upstream execution.

- [ ] Explicit OMP on `agy/` activates ProtectedAGY even when generic model
  prefixes exclude it. Verify marker consumption and request/stream cloaking.
- [ ] Explicit OMP on a configured non-`agy/` route consumes the marker but
  preserves tool names and brand content across request and response/stream.
- [ ] Validate all accepted marker aliases: `oh_my_pi`, `omp`, `oh-my-pi`.
- [ ] Invalid protected admission (including declaration final-base collisions)
  returns the specified exact 503 `omp_cloak_required` response with **zero
  upstream attempts**. Cover namespace collisions using the admission contract
  in [CONTEXT.md](../CONTEXT.md).
- [ ] Request-scoped reverse mapping restores only declared/transformed pairs,
  including namespace-exact restoration. Native AGY target-only and inactive
  targets are not incorrectly reversed; tool choice and correlated history
  remain coherent. Run concurrent/disjoint tool inventories for isolation.
- [ ] Protected brand rewrite and assistant-text restoration work; literal
  `.omp` paths, tool argument values, and control metadata retain their contract.
- [ ] Follow the [sanitization spec](specs/system-conventions-sanitization.md):
  exact opening/closing tags become `<conventions>` in supported system text;
  other roles, tool payloads, metadata, and bypass routes remain unchanged.
  Decode JSON first: upstream tags may appear as `\u003c...\u003e` in raw logs.
- [ ] Record offline coverage for fragmented SSE, terminal flushes, pinned route
  cleanup on `request.complete`, and interleaved requests. A single successful
  live stream does not prove all chunk boundaries or absence of state leaks.
- [ ] Run a separate smoke through the unchanged default OMP profile and record
  its actual endpoint/model; do not label local `cloak-live` evidence as a
  default-profile test.

## 6. Record the verdict and clean up

Copy this worksheet into a local evidence note. Add one row per canonical case,
transport variant, exposed optional tool, and protocol check.

| Case | Status | Request IDs / tool-call IDs | Actual execution and continuation | Redacted evidence / blocker |
| --- | --- | --- | --- | --- |
| OMP-01 | NOT RUN | | | |

- [ ] Record canonical count `__/9`, transport results, optional inventory with
  per-tool results/N/A reasons, protocol results, and default-profile status.
- [ ] State full acceptance only when all required rows pass. Otherwise report
  partial coverage and list failures, unavailable tools, and untested cases.
- [ ] Restore temporary profile/provider settings and follow the deployment
  runbook cleanup on success **or failure**: disable debug, restore request-log
  state, recreate, truncate only this run's raw logs, and verify gateway health
  and installed plugin/hash again.
- [ ] Keep redacted results and checksums; do not commit credentials, auth files,
  full debug bodies, or OMP transcripts containing private context.
