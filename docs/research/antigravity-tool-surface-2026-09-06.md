# Antigravity tool surface and cloak implications

## Conclusion

For `antigravity-cloak`, use the live AGY CLI runtime as the canonical tool-name source. The Gemini API Antigravity Agent documentation describes a higher-level managed-agent tool surface (`code_execution`, `google_search`, `url_context`, filesystem, custom `function`, and `mcp_server`) and does not define the AGY CLI harness-level names used by this plugin (`run_command`, `view_file`, `invoke_subagent`, etc.).

Do not optimize for 100% OMP tool-name coverage. Maintain a small one-to-one Safe Mapping Set whose targets are verified AGY-native tools with matching semantics and no reverse-map collision. OMP tools outside that set may intentionally pass through unless a future protected-route policy explicitly restricts them.

The `cloak-live` profile should be the exhaustive acceptance harness for every Safe Mapping Set entry, mixed mapped/unmapped/MCP requests, streamed reverse mapping, and fail-closed behavior. Only after that gate passes should the default OMP profile receive a real smoke test.

## Findings

### Verified facts from Google documentation

- The Gemini API Antigravity Agent is a managed agent using the same harness family as Antigravity IDE, but its public tool contract is capability-oriented rather than the AGY CLI tool namespace.
- Default managed-agent tools are `code_execution`, `google_search`, and `url_context`; filesystem tools are enabled with an environment. Custom `function` tools and remote `mcp_server` tools are supported.
- The `tools` parameter can restrict or replace the default managed-agent tool set.
- Filesystem operations can appear as function calls in the managed-agent interaction trace even though the environment executes them automatically.
- `system_instruction` and file-based `AGENTS.md` instructions are additive.
- Managed agents can be based on `antigravity-preview-05-2026`; their model is configured through `agent_config` and, once persisted as a named agent, is fixed for that agent definition.
- Managed Agents are preview and currently state that subagent nesting is not supported. This differs from the AGY CLI runtime observed locally, which exposes subagent tools; therefore the managed-agent docs cannot be treated as an exact CLI tool-name schema.

### Project observations

- Current OMP mappings in `CONTEXT.md` include several targets that the live AGY CLI 1.1.27 runtime did not expose as native tools during direct introspection, including `execute_code`, `wait`, `cancel`, `list`, `multi_replace_file_content`, `update_plan`, `list_permissions`, `create_goal`, and `update_goal`.
- The live AGY CLI 1.1.27 runtime exposed the following native names during direct introspection: `view_file`, `run_command`, `manage_task`, `send_message`, `invoke_subagent`, `define_subagent`, `manage_subagents`, `write_to_file`, `replace_file_content`, `generate_image`, `read_url_content`, `search_web`, `find_by_name`, `grep_search`, `list_dir`, `ask_question`, `call_mcp_tool`, `list_resources`, `read_resource`, and `schedule`.
- Current OMP upstream has a larger built-in inventory than the cloak table. With `tools.xdev=true` by default, many discoverable tools are mounted behind `xd://` and transported through `read`/`write`; essential top-level tools remain directly exposed.
- The default OMP profile now sends `X-Cloak-Client: oh_my_pi` on provider `cpa`, providing deterministic client identity. The `cloak-live` profile remains the isolated local acceptance profile.
- The active default OMP profile does not override `edit.mode`, `task.batch`, or `tools.xdev`; current upstream defaults therefore make `edit` use the `hashline` wire shape, `task` use the batch `{ context, tasks[] }` wire shape, and `tools.xdev=true`. The profile explicitly has `web_search.enabled: false`.
- `antigravity-cloak` is a rename-only adapter for tool identity: it rewrites tool names and textual references but does not translate each tool's argument schema. The model therefore continues to receive and call the OMP source schema under the cloaked AGY-facing name, and the response path restores the original OMP name before execution.

### Schema-level comparison for OMP candidates

| OMP source | Current OMP model-facing shape | AGY CLI native target shape | Decision |
| --- | --- | --- | --- |
| `read` | `{ path: string }`; multiplexes local file, directory, URL, internal URI, and `xd://` transport | `view_file` primarily uses `AbsolutePath` plus optional line/range fields; AGY splits directory/URL/resource reads into other tools | Keep as a deliberate transport exception; not an exact 1:1 semantic match |
| `write` | `{ path, content }`; writes files and also dispatches `xd://<tool>` virtual devices | `write_to_file` is a file-write primitive with `TargetFile`, `CodeContent`, overwrite/metadata fields | Keep as a deliberate transport exception because OMP uses it as an essential `xd://` carrier |
| `edit` | Default profile uses hashline mode, model-facing `{ input: string }` | `replace_file_content` is a contiguous replacement primitive with explicit file/range/content fields | Keep: dominant semantic is file mutation; schema remains intentionally OMP-native |
| `bash` | `{ command, env?, timeout?, cwd?, pty?, async? }` | `run_command` executes a shell command and can transition to managed background execution | Keep: direct dominant semantic match |
| `grep` | `{ pattern, path?, case?, gitignore?, skip? }` | `grep_search` searches file content using regex/search options | Keep: direct dominant semantic match |
| `glob` | `{ path?, hidden?, gitignore?, limit? }`; name/glob/path search | `find_by_name` searches files/directories by pattern; `list_dir` only lists one directory | Keep, but target must be `find_by_name`, not current `list_dir` |
| `task` | Default batch shape `{ context, tasks[] }`; one or more subagent spawns | `invoke_subagent` accepts one or more subagent definitions and launches them | Keep: direct dominant semantic match despite different field names |
| `ask` | `{ questions[] }` with option items and multi-select metadata | `ask_question` presents structured user questions | Keep: direct dominant semantic match |
| `todo` | Structured todo-state machine (`init/start/done/rm/drop/block/...`) | No AGY todo tool; AGY task tracking is a task artifact protocol, while `manage_task` controls background processes | Remove from static mapping |
| `hub` | One schema covering peer messaging, async jobs, and supervised process lifecycle | AGY splits this across `send_message`, `run_command`/`manage_task`, `manage_subagents`, and sometimes `schedule` | Remove from static mapping; no single safe 1:1 target |
| `web_search` | `{ query, recency?, limit?, max_tokens?, temperature?, num_search_results? }` | `search_web` performs web search with query/domain-style controls | Keep when the source tool is exposed; current default profile disables it |
| `eval` | Persistent JS/Python evaluation kernel | No equivalent AGY-native persistent evaluation tool | Remove from static mapping |

AGY CLI self-introspection independently confirmed the three ambiguous cases: local-file `read` maps naturally to `view_file` but directory/URL/virtual-resource reads fan out to other AGY tools; `glob` maps most closely to `find_by_name`; and `hub` has no single safe AGY rename because its operation families fan out across several native tools.

## Final Safe Mapping Set

The static rename set for OMP should be exactly:

| OMP | AGY CLI target | Classification |
| --- | --- | --- |
| `read` | `view_file` | transport exception; exhaustive live coverage required |
| `write` | `write_to_file` | transport exception; exhaustive live coverage required |
| `edit` | `replace_file_content` | semantic alias; source schema remains OMP-native |
| `bash` | `run_command` | direct semantic alias |
| `grep` | `grep_search` | direct semantic alias |
| `glob` | `find_by_name` | direct semantic alias |
| `task` | `invoke_subagent` | direct semantic alias |
| `ask` | `ask_question` | direct semantic alias |
| `web_search` | `search_web` | direct semantic alias; only relevant when OMP exposes it |

Static mappings explicitly excluded from the final set: `todo`, `hub`, `eval`, all `vibe_*` mappings, and the Autoresearch mappings (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`). These remain intentional pass-through unless a later design introduces a real schema-transforming adapter or suppresses them on a protected route.

For the two transport exceptions, acceptance must cover the multiplex behavior rather than only a local-file happy path: `read` must exercise local file, directory, URL/internal-resource, and `xd://` reads; `write` must exercise local file write and an `xd://` device dispatch. The final mapping is accepted only if those calls survive OMP -> cloaked AGY-facing name -> streamed reverse-map -> OMP execution without duplicate/ambiguous tool calls.

## Implications for this repo

- Treat Google managed-agent docs as architectural evidence, not as the exact AGY CLI mapping table.
- Define the Safe Mapping Set by unique reverse mapping plus dominant semantic equivalence to a live AGY-native target. Exact field-name/schema equality is not required because the current plugin deliberately preserves the OMP source schema and only cloaks the tool identity.
- Adopt the final OMP Safe Mapping Set documented above: `read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, and `web_search -> search_web`.
- Treat `read` and `write` as explicit transport exceptions because OMP multiplexes non-file `xd://` behavior through them. Their inclusion is conditional on exhaustive `cloak-live` round-trip acceptance for every supported path/device class.
- Remove `todo -> manage_task`: AGY `manage_task` manages background processes, not checklist state. AGY task tracking is represented as a task artifact written with `write_to_file` (`IsArtifact: true`, `ArtifactMetadata.ArtifactType: "task"`) and updated with file-edit tools. Because that is a multi-step protocol and would collide with the existing `write -> write_to_file` reverse mapping, it is not a safe static one-to-one cloak mapping.
- Remove `hub -> send_message`: the OMP `hub` schema combines messaging, job control, and process supervision, while AGY divides those behaviors across several tools. A static rename would misrepresent most of the source schema.
- Remove stale/non-native targets rather than inventing near-equivalent aliases; this includes the current `eval`, Vibe, and Autoresearch aliases unless a future schema-transforming adapter is designed and proven.
- Do not require 100% OMP tool coverage. Intentional pass-through is preferable to ambiguous reverse mappings or stale AGY target names.
- Fail-closed should apply to a protected OMP request when the cloak pipeline itself cannot establish its required transformation/correlation guarantees; absence of a mapping for an intentionally pass-through tool is not, by itself, a cloak failure.
- Acceptance must exercise all entries in the Safe Mapping Set through real `cloak-live` OMP -> local CLIProxyAPI -> upstream -> streamed response -> OMP execution, plus mixed mapped/unmapped/MCP cases and failure injection proving zero upstream execution when the cloak pipeline is required but broken.

## Open questions

- Which intentionally unmapped OMP top-level tools, if any, should be suppressed or rejected on the protected AGY route rather than passed through unchanged.
- Exact host-level mechanism for guaranteeing fail-closed behavior if the cloak plugin itself is unavailable, panics, or is fused by CLIProxyAPI.
- Whether the `read`/`write` transport exceptions pass the full `cloak-live` matrix (file, directory/resource/URL where applicable, and `xd://` device traffic) without duplicate/ambiguous calls; failure of that gate removes the failing exception from the Safe Mapping Set rather than adding heuristic aliases.

## Sources

- Google AI for Developers — Antigravity Agent: https://ai.google.dev/gemini-api/docs/antigravity-agent
- Google AI for Developers — Building Managed Agents: https://ai.google.dev/gemini-api/docs/custom-agents
- Antigravity system prompt leak used only as supplemental evidence: https://github.com/asgeirtj/system_prompts_leaks/blob/main/Google/antigravity-cli.md
- Superpowers Antigravity tool mapping reference used only as secondary corroboration for AGY task-artifact semantics: https://github.com/obra/superpowers/blob/main/skills/using-superpowers/references/antigravity-tools.md
- Local AGY CLI 1.1.27 direct runtime introspection performed on 2026-09-06.
- Local OMP upstream mirror: `.ref/oh-my-pi`.
- Local CLIProxyAPI upstream mirror: `.ref/CLIProxyAPI`.
