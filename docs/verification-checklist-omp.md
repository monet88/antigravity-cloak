# OMP Full Tool-Cloak Verification Checklist

Use this checklist to verify the installed plugin through the **real OMP client**.
The canonical mapping contract is [CONTEXT.md](../CONTEXT.md). Build, install,
profile setup, debug capture, cleanup, and rollback follow the
[local deployment runbook](verification-checklist.md). Complete its preparation
before running this matrix, and its cleanup after every controlled debug batch.

## Current evidence and acceptance rule

The recorded v0.5.1 run on 2026-09-12 proved tag sanitization and the
`bash -> run_command -> bash` execution/continuation path. The historical
2026-09-07 nine-tool run used a different plugin/OMP version. The current
results are in [Verified results - 2026-09-27](#verified-results---2026-09-27-plugin-v060).
Every case was produced by the real OMP client; where the client itself requires
an environment this workstation does not have by default (`ask` needs an
interactive session, `web_search` needs a provider), the case records the
supported path that made it runnable.

The rule below governs a fresh run of the matrix in section 3, not the dated
record in the next section, which is fixed historical evidence and never reset.

Every result in a fresh run starts **NOT RUN**. Record PASS, PASS (escaped
wire), FAIL, BLOCKED, or NOT RUN with evidence. A prompt requesting a tool, a
declaration rewrite, an HTTP 200, or a model claiming success is insufficient.
Full canonical acceptance requires all nine tools to execute successfully on the
pinned binary **under their bare spelling**; `PASS (escaped wire)` means the tool
executed only through its `_`-prefixed spelling and is therefore not bare
acceptance. An unavailable canonical tool is BLOCKED, not PASS.
Unexposed extended tools may be N/A only with a recorded
inventory/configuration reason; any exposed or declared
extended tool must be cloaked to its assigned alias and restored downstream
rather than skipped as pass-through.

## Verified results - 2026-09-27 (plugin v0.6.0)

Pinned run: source `4e946ac` (clean worktree), artifact SHA256
`a9b4e88833784e14063e25b0716bedbdebe91a615386394e0bd6ca4153ca013b`
(`vcs.revision=4e946ac`, `vcs.modified=false`, linux/amd64 CGO c-shared, GLIBC
requirement ≤ 2.34), installed as `antigravity-cloak-v0.6.0.so`. Real OMP
`18.3.4` on the isolated `cloak-live` profile
(`http://127.0.0.1:8317/v1`, `X-Cloak-Client: oh_my_pi`), model
`cpa/agy/gemini-3.8-flash`. Correlated evidence lives in the local evidence area
under `.git/` and in the gateway request logs; nothing raw is committed.

| ID | Status | Correlated result |
| --- | --- | --- |
| OMP-01 `read` | PASS | Ingress `read` → upstream `view_file` → client `read`; returned exactly `alpha`/`beta` |
| OMP-02 `write` | PASS | `write` → `write_to_file` → `write`; created `written.txt` with `OMP_WRITE_OK`, read back |
| OMP-03 `edit` | PASS | `edit` → `replace_file_content` → `edit`; OMP hashline patch applied (`1:before` → `1:after`) |
| OMP-04 `bash` | PASS | `bash` → `run_command` → `bash`; `printf OMP_BASH_OK` executed, continuation followed |
| OMP-05 `grep` | PASS | `grep` → `grep_search` → `grep`; matched `sample.txt` line 2 |
| OMP-06 `glob` | PASS | `glob` → `find_by_name` → `glob`; matched the real fixture file |
| OMP-07 `task` | PASS | `task` → `invoke_subagent` → `task`; child `ReadChild` completed (19.4 s) and the parent read `agent://ReadChild` → `{"content": "alpha\nbeta\n"}` |
| OMP-08 `ask` | PASS (escaped wire) | Not exercised under the bare spelling. The only run that reached it used `_ask` → `ask_question` → `_ask` over `anthropic-messages` (OMP-ESC-05): the Ask overlay rendered in a real interactive TUI session (pty), the operator selected "Green", and the continuation carried `ask_question` with `User selected: Green` upstream while the client history kept `_ask`. Bare `ask` is NOT RUN: headless `-p` runs never declare it — the client registers the tool through `createIf`/`canPromptUser`, and `execute` throws "Ask tool requires interactive mode" without a UI |
| OMP-09 `web_search` | PASS (escaped wire) | Not exercised under the bare spelling. The only run that reached it used `_web_search` → `search_web` → `_web_search` over `anthropic-messages` (OMP-ESC-04), with `providers.webSearchOrder: [exa]` enabled reversibly in the acceptance profile (key already present in the environment); it returned live results and continued. Bare `web_search` is NOT RUN |

Canonical execution count: **7/9 bare-canonical executed end-to-end**
(`read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`). `ask` and
`web_search` are recorded as escaped-wire only.

Escaped-canonical wire (`api: anthropic-messages` in the acceptance profile,
reverted afterwards; the client emits the `_`-escaped builtin spelling only
through its Anthropic client module):

| ID | Status | Correlated result |
| --- | --- | --- |
| OMP-ESC-01 `_read`/`_bash` | PASS | ingress `_read`,`_bash` → upstream `view_file`,`run_command` → downstream `_read`,`_bash`; the client ran `read` on the fixture and `bash "echo ESCAPE-OK"`, then continued to a final summary (exit 0) |
| OMP-ESC-02 shared + fallback | PASS | ingress `_todo`,`_find`,`_wait` → upstream `wp_todo`, `wp_ext_8bfa04d75e222553a5dc712e5ec3671e`, `wp_ext_416dac4969d214f84545c92795d9a734` → restored verbatim; both hashes recomputed from `fallbackAliasForSource`; `todo`, `find` and `wait` all executed |
| OMP-ESC-03 `xd://` device | PASS | ingress `_read` with `path: xd://todo` → upstream `view_file` with the same `path: xd://todo` byte-identical → downstream restored with the URI intact; the client dispatched to the `xd://todo` device and returned its output |
| OMP-ESC-04 `_web_search` | PASS | ingress `_web_search` → upstream `search_web` → restored; live results returned, continuation followed |
| OMP-ESC-05 `_ask` | PASS | ingress `_ask` → upstream `ask_question` → restored; TUI answer flow as in OMP-08 |

Transport and alias checks:

| ID | Status | Result |
| --- | --- | --- |
| EXT-03 `read` virtual device (`xd://<top-level-tool>`) | PASS | The acceptance profile mounts no standalone device, and a bare `xd://bash` correctly resolves to "No such tool"; the client also dispatches active top-level tools, so `read` with `path: xd://todo` executed against the todo device with the URI intact (see OMP-ESC-03) |
| EXT-04 `write` virtual device | PASS | `write` to `xd://bash` dispatched the real bash runner (`echo xd_write_dispatch_success`) with the `xd://` identifier intact through `write_to_file → write` |
| Shared aliases | PASS | bare `todo → wp_todo` and `find → wp_find`, plus escaped `_todo → wp_todo`; all restored to the client spelling and executed |
| Dynamic/fallback | PASS | bare `wait → wp_ext_061bef0f1c6ccd0b4819958bcb73eba6` and `yield → wp_ext_6000f482bcb616c9b358f46162f191f6`, plus escaped `_find → wp_ext_8bfa04d75e222553a5dc712e5ec3671e` and `_wait → wp_ext_416dac4969d214f84545c92795d9a734`; the alias is keyed on the source identity as sent, so the escaped spelling gets its own deterministic hash |

One `agy/gemini-3.8-flash` request carried these 12 declarations and produced
this upstream set, with no source name surviving upstream:

```
ingress : bash edit eval find glob grep read task todo wait web_search write
upstream: find_by_name grep_search invoke_subagent replace_file_content
          run_command search_web view_file wp_eval
          wp_ext_061bef0f1c6ccd0b4819958bcb73eba6 wp_find wp_todo write_to_file
```

System-prompt sanitization was confirmed on the same request:
`<system-conventions>` counts 2 → 0 and `<conventions>` 1 → 3 (ingress →
upstream), decoded from JSON rather than by raw substring counting.

Protocol checks (local, decided before upstream dispatch):

| Case | Result |
| --- | --- |
| Conflicting marker `oh_my_pi, claude_code` on `agy/` | PASS: HTTP 503 `omp_cloak_required`, no upstream attempt |
| Declaration collision (`read` plus a native `view_file`) | PASS: HTTP 503 `omp_cloak_required` |
| Explicit OMP marker on a non-`agy/` route | PASS: zero mutation (`bash` ingress, `bash` upstream) |

Default-profile smoke (profile unchanged, its own remote gateway endpoint and
model role unchanged — discover both from the profile rather than pinning them
here): `read` on the fixture executed natively and returned `alpha\nbeta`. This
exercises the default production path and the remote deployment — it is **not**
evidence about the local artifact, since the default profile does not point at
the local gateway.

Honest gaps in this pass:

- `ask` is reachable only from an interactive session and `web_search` only with
  a search provider; both were run through those supported paths. Neither is
  reachable from a headless default-profile `-p` run, so the headless smoke
  stays limited to tools the client exposes there.
- Escaped canonical spellings require the `anthropic-messages` wire: on
  `openai-completions` the stock client declares bare `read`. The escaped run
  pinned `api: anthropic-messages` and the bare run pinned
  `openai-completions` in the acceptance profile; both were reverted afterwards.
- Residual payload identifiers: OMP's own subagent payload retained the upstream
  spelling inside prose (`... using view_file or appropriate tool`), because the
  reverse rewrites tool-name positions and not free text inside tool arguments.
  This is inherent to the rename mechanism and is not a claim of full harness
  anonymity.

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
