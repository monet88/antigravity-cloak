# Spec: OpenAI Responses Wire Surface (cloak + reverse)

Status: **approved specification — offline implementation complete.**
The offline implementation and unit/lifecycle regression suite described by this specification are present in the repository (`main.go`, `responses_surface.go`, `issue53_responses_test.go`). Live rollout observations (U1, U5), gateway deployment, and live gateway configuration changes remain separate rollout actions (§12).
Companion contracts: `CONTEXT.md` §1 (Brand Rewriting) and `docs/specs/system-conventions-sanitization.md`.

**Evidence and compatibility profile, amended 2026-11-08**

- Plugin baseline: `main@5114678b220c0f902db88ffa9e7bd89248e4c715` (v0.6.0).
- Plugin SDK build dependency: `github.com/router-for-me/CLIProxyAPI/v7 v7.2.143`, as recorded in `go.mod:5`. Its tag resolves to `4b5f1eab25fca4b3815369a826e958e7c070a69e`. This dependency is unchanged.
- Host protocol reference: **CLIProxyAPI v8.0.4**, exact commit `d33f63f8e3d98428440ebca5a5b6a981a61ff71e`. Host source anchors below refer to that commit, not to the SDK dependency. The local `.ref/CLIProxyAPI` checkout identifies this reference; its relevant files were compared with the pinned commit.
- The v7 SDK pin does not pin the running host's translators. In particular, v7.2.143 does not contain the v8 Chat name-cap/index/disambiguation contract used below. This spec claims no v7 host parity and performs no upgrade. Verify the deployed host before rollout (§12); no automatic host-version detection or compatibility registry is introduced.
- The referenced host has two evidenced lowering paths relevant to identity admission: Responses → Chat Completions and Responses → Gemini/Antigravity. Native Codex → Responses also passes client-facing events through. These paths must not be conflated. Existing Chat Completions and Anthropic client behavior remains the regression baseline.
- Historical workstation observations in `docs/research/codex-tool-surface-2026-09-12.md` are dated observations, not a new live verification.

## 1. Problem and intended outcome

The host supplies the raw client request body to `request.intercept_before` before translation (`sdk/api/handlers/handlers_execution.go:67`; `handlers_interceptors.go:469`). The current plugin normalizes `openai-response` and `codex` to `openai` but its OpenAI request/tool readers expect Chat Completions. A Responses flat declaration therefore either supplies no detection evidence or reaches the alias-plan collector's unsupported-declaration rejection. That is a missing wire surface, not evidence that fail-closed admission should be removed.

Teach the existing request, non-stream response and streaming paths the Responses shape. Reuse the existing client resolution, brand policy, alias plan, argument-safe reverse and lifecycle ownership. Every supported transformed tool identity must restore through the request's own plan. Preserve user prose, reasoning, executable argument literals outside the existing argument-safe rules, provider-built-in payloads and unrelated fields.

The dated Codex surface research records an opencodex adapter that lowered this workstation's traffic to Chat Completions. This change is useful for direct Responses-speaking clients; it makes no claim that the current workstation or production deployment needs it. U1 is a rollout observation.

## 2. Wire families and dispatch

Each JSON payload is classified once, using an explicit direction. `handleRequestInterceptBefore` calls `requestWireFamily`; `handleResponseIntercept` calls `responseWireFamily`; `handleStreamChunkIntercept` calls `streamEventFamily` on each decoded event payload.

`normalizeSourceFormat` remains unchanged. Its result first selects the existing major protocol family. Within normalized `openai`, shape selects Chat Completions or Responses. An Anthropic body is never sent into the OpenAI shape discriminator merely because it contains `messages`. The model gate still uses `Model`/`RequestedModel`, and client-control precedence stays unchanged. `SourceFormat` does not identify the upstream translator; `ToFormat` is not populated at this before-auth seam.

| Normalized format | Family | Request discriminator | Non-stream response discriminator | Stream JSON discriminator |
| --- | --- | --- | --- | --- |
| `anthropic` | `anthropic` | existing format-directed path | existing path | existing path |
| `openai` | `openai-chat` | root `messages` is an array | root `choices` is an array | root `choices` is an array |
| `openai` | `openai-responses` | root `input` is an array/string, or `instructions` is a string | root `output` is an array, or `object == "response"` | root `type` is a string beginning `response.` |
| other | `unknown` | — | — | — |

Rules:

1. Within one direction, both family discriminators or neither means `unknown`. Empty arrays/strings still have the stated type and count. `object:"response"` is never a request discriminator.
2. Nested objects are not reclassified. A `response.completed` event is classified from its root; its `response` object is walked as Responses.
3. `unknown` means no body walker, no body-based client detection and no speculative retry with another family. Its body is preserved byte-for-byte when the request is otherwise eligible for pass-through. On streaming callbacks that need §5.1 framing reconstruction, preserve the unknown JSON data bytes exactly; adding required SSE separators is not permission to reserialize that data.
4. Classification does not override earlier control-plane obligations: consume `X-Cloak-Client`, preserve explicit OMP non-AGY durable bypass, and enforce ProtectedAGY/correlation/admission failures. If request control authority already establishes an alias-plan client and its body cannot be safely admitted, retain the existing 503 `tool_cloak_required` behavior; do not turn a known client's admission failure into an unknown-body bypass. ProtectedAGY rejects Responses and unknown shapes (§6).
5. A root `{"error":…}` or `{"type":"error",…}` without a Responses discriminator is unknown. A plugin-visible `response.error` has the Responses prefix and can be handled as terminal-only (§5).
6. An SSE `event:` line is framing, not a substitute for the JSON discriminator. Reuse the handler's existing SSE and raw-JSON chunk paths; native converters need not supply the same framing as Chat conversion. The optional `[DONE]` sentinel is existing framing control, outside the JSON classifier.
7. Declaration field selection is separate from payload classification. On Responses declarations, both evidenced converters read a nonblank trimmed top-level `name` first, then trimmed `function.name`. A `function` object merely existing does not override a flat name. Collectors and writers must select the same effective field.

Representative fixtures include request `messages`, request `input`, instructions-only request, mixed `messages`/`input`, tools-only request, response `choices`, response `object:"response"`/`output`, mixed `choices`/`output`, and a terminal event whose nested response has both `object` and `output`. A response with `object:"response"` and no `output` is still Responses; its carrier walk is a no-op.

## 3. Request surface: client → upstream

### 3.1 System and conversation carriers

| Carrier | Treatment |
| --- | --- |
| `instructions` string | Existing system-scope brand policy plus declared-tool prose aliases. |
| `input[]` message with `role:"system"` or `"developer"` | Same system policy for content strings and supported text parts. |
| `input` plain string | User turn; preserve verbatim. |
| `input[]` message with `role:"user"` | Existing `rewriteUserTurnContent` semantics: typed prose/tool names remain literal; only recognized `<system-reminder>` spans and tool-result carriers receive the existing rewrite. |
| `input[]` message with `role:"assistant"` | Existing assistant-history brand and declared-tool prose-alias policy. |
| `function_call` / `custom_tool_call` | Resolve the callable identity under §3.4 before deciding its name mutation. Preserve request `arguments` / `input` and `call_id`. |
| `function_call_output` / `custom_tool_call_output` | Existing machine-produced tool-result brand/prose-alias policy; preserve `call_id`. |
| `additional_tools` item | Declaration source under §3.2, never a conversation message. |
| `reasoning` item and unrelated item types | Preserve. Do not run a generic all-string rewrite. |

Messages may omit `type` when `role` identifies the message. Reuse the existing text-carrier predicates; Responses input text includes `input_text` and the host's untyped text-part form. Assistant-visible output text parts use `walkOpenAITextParts` and its existing allowlist. Never let a part's permissive text type override a containing `reasoning` item.

Text-level tool alias authority remains the plan's **declared** forward table. A history-only or selector-only fallback does not gain new prose replacement authority.

### 3.2 Declarations and namespace policy

Collect top-level `tools[]` first, then each `input[]{type:"additional_tools"}.tools[]` in input order. Visit children in declaration order. Function declarations may be flat or nested; `type` may be absent. Custom declarations use `type:"custom"`.

| Declaration | Mutation |
| --- | --- |
| Flat function, including effective `function.name` form | Alias the effective name using the request plan. Apply existing brand/prose-alias rewriting to description and schema description/title fields. |
| Flat custom | Alias the effective name and rewrite description; preserve `format`. |
| Nonempty namespace container | Container name and child declarations stay pass-through. They contribute to detection and admission safety, not alias/reverse authority. |
| Empty/absent namespace name | Its callable children have effective empty namespace and follow the flat rule. |
| Provider built-in type, e.g. `web_search`, `file_search`, `mcp`, `local_shell` | Preserve; it owns no client tool identity. |

A namespace's normal child carrier is `tools`. The Gemini/AGY collector also accepts `children` when `tools` is not an array (`internal/util/responses_tools.go:134`). Include that carrier in the inventory and apply the same namespace mutation policy. The Chat projection must still omit it as the Chat converter does. Do not hide a Gemini-visible declaration from safety checks by using a Chat-only prefilter.

Both converters choose the effective declaration name as described in §2. Record the authoritative occurrence; rewrite that name field, not an ignored fallback field. Preserve every schema key, property name, `required`, `enum`, `const` and `pattern`. Reuse `isSchemaTextField`; no recursive arbitrary-string transformation.

Nonempty namespace children deliberately remain visible to the provider. This is a supported limitation, not a claim of total tool concealment. Their history references and output identities need the same namespace-aware treatment. A bare history name can refer to a namespace child even when it has no sibling `namespace`; absence of that field is not permission to alias it.

The Chat converter qualifies a namespace child after interception, including its `mcp__`, already-qualified and child-equals-namespace exceptions, then caps the emitted name. Its response index returns the declaration's local name and namespace. Leaving these declarations intact avoids adding namespace renaming to this change. Gemini likewise retains reverse local-name/namespace identity after its own sanitization.

For a flat alias target, require `capResponsesChatToolName(target) == target` under the pinned Chat rule: a 64-**byte** limit, tail retention and leading `_`/`-` trimming only after truncation. A target that this rule changes fails 503 `tool_cloak_required` before dispatch. Do not emit a truncated alias or silently leave the flat source unmasked. Operator tool aliases are configured through **`tool_mappings`**, not brand `custom_mappings`. This request admission rule adds no new configuration parser validation.

Gemini sanitization need not be a no-op: its reverse identity map returns the post-intercept local name. Evaluate its actual sanitization/disambiguation in the owner checks below rather than adding an unnecessary operator-alias restriction.

### 3.3 Source identity, repeats and alias authority

A source declaration identity is the case-sensitive pair `(effective namespace, effective localName)`, with the host's declaration whitespace normalization. Type, description and schema do not create a second identity for a repeated name pair.

Repeated occurrences, including one in `tools` and one in `additional_tools` with different description/schema, receive **one alias decision**. Rewrite every authoritative name occurrence consistently. Preserve the host's winning declaration and its original schema/description precedence; do not reject a same-source repeat as an alias collision.

Distinguish this logical source pair from an **effective callable owner**. Different source pairs can already flatten onto the same upstream name. The winning declaration is then converter-dependent, and response identity comes from that winner. Attach stable original source identities to tentative rewritten declarations so before/after owner comparison does not compare an original name with its alias.

Reuse the current alias-plan authority for static, shared and deterministic `wp_ext_<hash>` fallback aliases. Every transformed declaration, supported history reference and supported selector consults that request's plan. Do not add independent selector/static lookups or response-side global inversion.

### 3.4 Resolve references and validate the proposed mutation

Before mutating, build the original inventory and proposed alias plan. Validate both the original and proposed bodies against the two **pinned** lowering rules. The before-auth envelope does not expose the chosen downstream converter (`handlers_interceptors.go:474`), so neither `SourceFormat` nor an empty `ToFormat` can select just one rule.

| Projection | Declaration winner and reference resolution |
| --- | --- |
| Responses → Chat | Follow `walkResponsesToolDeclarations`, qualification, cap and `disambiguateResponsesChatToolNames`. The index first-wins by emitted name. For an explicit namespace, `namespaceName` resolves the tuple to an emitted name; otherwise `canonicalName` checks emitted name, raw qualified name, then unique local name, with collision-avoiding fallback. The callable owner is the winning `byChat` declaration at that emitted name, not merely a successful tuple lookup. |
| Responses → Gemini/AGY | Follow `CollectResponsesToolDescriptors`, `CollectResponsesToolWinners` and `BuildGeminiFunctionDeclarations`. Winner priority is source tier, then direct declaration over namespace child within that tier, then order. Follow the qualified/local forward aliases, `SanitizeFunctionName` and hashed sanitization disambiguation. `MapResponsesToolName` resolves the actual emitted name; its reverse identity map identifies the owner. Local aliases here are not Chat's unique-local rule. |

These are two concrete admission checks, not a provider framework. Preserve each converter's own existing behavior:

`ChatOwner(before) == ChatOwner(after)` and `GeminiOwner(before) == GeminiOwner(after)`.

The two converters need not agree with each other. The outcome is a stable original winning owner, or unresolved; unresolved must not become a different declared tool after aliasing.

Reference disposition:

- A reference to a transformed flat owner uses that owner's planned target. If its consumed name field already uses another host-accepted spelling of that owner, choose the target from the declaration, not from a new fallback hash of the spelling.
- A namespace reference remains pass-through, including qualified/bare-local references with no sibling `namespace`. Where the original projections disagree, keep the original reference unless one concrete rewrite preserves both outcomes.
- A name matching namespace declarations but ambiguous in Chat is **not truly undeclared**. In particular, two namespace children named `exec` may be unresolved in Chat and first-wins in Gemini. Preserve that reference and do not manufacture a fallback alias just because one projection failed to resolve it.
- A truly undeclared, supported history/selector reference retains the existing fallback policy only after both projections and the declaration inventory establish that it is not a reference to a declared callable. Its fallback is request-scoped; it gains no declaration/prose authority.
- Reject 503 `tool_cloak_required`, with zero upstream attempts, if the proposed mutation changes any affected reference's original owner, resolves a formerly unresolved reference onto a declared tool, or cannot keep declaration/reference treatment consistent. Do not silently add a namespace or reshape the selector to guess intent.

Collision admission also considers **all** declarations, including pass-through children and native target names, in each applicable projection. Compare distinct source owners, not repeated occurrences. Reject a collision or change of the winning callable/schema/type caused by a rewritten target, including an original collision whose ownership would change when a flat name is renamed. Preserve and log existing collisions/ambiguities involving no mutation. The Chat safety check detects distinct owners sharing a base emitted name before host suffixing; it must not guess which alias the host would award. Gemini's sanitation/hash handling remains part of its projection and is not itself a rejection when ownership is preserved.

Concrete required cases:

1. Namespace `tools`, child `tools__exec`, history `name:"tools__exec"` without `namespace`: declaration and reference both remain pass-through.
2. Flat `exec` plus namespace `tools/exec`, bare selector `exec`: the flat declaration wins; selector follows its `run_command` alias.
3. Flat `exec → run_command` plus namespace `ns/run_command`, bare selector `run_command`: aliasing would change the namespace owner to the flat owner; reject. The explicitly namespaced selector remains valid.
4. Flat `editor__apply_patch` and namespace `editor/apply_patch`: preserve each converter's original winner or reject an interacting mutation. Chat's first-wins and Gemini's direct-declaration priority must not be collapsed into one rule.
5. Same tool repeated across declaration channels: one alias, all occurrences consistent, original winning schema retained.

### 3.5 `tool_choice` and recorded restoration

Supported named selectors are `type:"function"` or `type:"custom"` with a string name in flat `name`, `function.name` or `custom.name`. Resolve the effective namespace from root `namespace`, then `function.namespace`, then `custom.namespace`, as the host does.

Chat reads names in nested-function, nested-custom, flat order; Gemini reads flat, nested-function, nested-custom order. For a selector with multiple populated name fields, allow identical name values and update every consumed field consistently. Reject conflicting populated values when identity mutation is planned. Do not invent a universal field precedence that disagrees with a converter.

String selectors (`"auto"`, `"required"`, `"none"`) remain unchanged. Other structured selector forms, including Gemini-only `type:"tool"` or absent-type shorthand, are outside named-selector support: if this request would rename tool identities, fail closed rather than send a stale selector; otherwise preserve the object. This is an explicit supported subset, not an undeclared-name fallback. It does not alter existing Chat/Anthropic selector behavior.

For a rewritten selector, record its original **effective name value**, the rewritten value and the fact of rewriting alongside the request plan. Reverse only that recorded value in the host echo's corresponding supported name carrier(s). Preserve the host's selector shape and other fields; do not restore the entire original JSON object.

Example: client `{"type":"function","name":"exec"}` can yield a non-stream chat-shaped echo `{"type":"function","function":{"name":"run_command"}}`. Return `{"type":"function","function":{"name":"exec"}}` there, and flat `name:"exec"` in a flat terminal echo. A selector that was not rewritten has no restore record and is untouched, even when its name equals an active flat reverse key. Never infer its original value from the reverse map.

## 4. Non-stream response surface: upstream → client

Restoration requires correlated request authority. An uncorrelated Responses body remains untouched and is logged. A recorded bypass remains a bypass. Identity reversal is exact and namespace-aware; brand reversal uses only the resolved client's existing table.

| Carrier | Action |
| --- | --- |
| `output[]` message → supported assistant text content | Existing brand reverse through `walkOpenAITextParts`. |
| Root `output_text` string, when present | Brand reverse. |
| `output[]` function call → `name` | Exact request-plan reverse only for a transformed flat identity. |
| `output[]` function call → `arguments` | Complete-fragment reverse using `argumentReverseTableFor` only. |
| `output[]` custom call → `name` / `input` | Same exact identity / argument-safe separation. |
| `output[]` reasoning → `summary`, `content`, `encrypted_content` | Preserve the entire reasoning item. |
| Echoed `instructions` | Existing system brand reverse; do not require the field. |
| Echoed `tools[]` | Exact declared-name reverse in either flat or nested shape; preserve namespace containers/children and built-ins. Existing brand policy may restore eligible description/schema text that the forward pass changed; do not reshape declarations. |
| Echoed `tool_choice` | Recorded name-value restoration under §3.5, only when this request rewrote it. |

A tool-call item with nonempty `namespace` is not a flat reverse candidate. For example, `{name:"run_command",namespace:"ns"}` must remain so even when this request also has `run_command → exec` in its flat reverse map. Preserve `id`, `call_id`, `namespace`, type and unrelated fields. A native target-only declaration with no active reverse entry stays literal.

Echoes are **provider-path-specific**:

- Chat → Responses non-stream reads the converted Chat request: `tools[].function.name` and `tool_choice.function.name`; it ordinarily has no `instructions`. Its terminal streaming builder prefers the original post-intercept Responses request.
- Gemini/Antigravity → Responses non-stream and terminal builders prefer the original post-intercept Responses request and can echo `instructions` plus Responses-shaped `tools`/`tool_choice`.
- Native Responses events may already be in the client shape.

Therefore the walker must accept both declaration/selector shapes and optional `instructions` independently of stream mode. It never compensates for a translator's chosen echo shape. Tool identities belong to the alias plan; prose and argument values belong to brand policy. No recursive all-string reverse is permitted.

## 5. Streaming surface

### 5.1 Typed event handling

Read each JSON event through the same family contract. The Responses framing seam must accept all three actual callback forms before invoking the typed walkers:

- Complete raw JSON callbacks.
- Full SSE frames and genuinely fragmented SSE/JSON bytes.
- Native scanner callbacks containing complete `event:` or `data:` lines **without trailing newlines**, including a data-only callback.

The native Codex executor uses `bufio.Scanner`, forwards the line bytes, and the native converter preserves that representation. Empty scanner callbacks are skipped before interception. Baseline `main.go:4229` concatenates tails and waits for the blank-line delimiter recognized at `main.go:4432`; it cannot be reused unchanged for this path. Otherwise complete native events accumulate without separators and never reach the new walkers.

Adapt this existing Responses framing seam, not the Chat/Anthropic buffer contract: retain a standalone event header until its complete data arrives, reconstruct the missing header/data line boundary, and deliver a complete valid JSON data callback exactly once without waiting for a blank callback, another event or `[DONE]`. A genuinely incomplete JSON payload remains buffered byte-for-byte until complete; do not insert a line break into a JSON string just because a continuation chunk starts with text such as `data:`. Existing fully framed SSE must neither be duplicated nor gain a second payload.

A framing-normalized output may add the required SSE separators while preserving unknown JSON data bytes exactly; it must not reserialize or rewrite an unknown payload. Keep framing tail state separate from semantic token carry. Synthetic carry flushes still precede the terminal's complete frame, including its retained event header. The pinned host's later HTTP framer and its separate native-line/partial-JSON fixtures provide the source oracle (`openai_responses_handlers.go:64`; `openai_responses_handlers_stream_test.go:223` and `:267`); the plugin cannot rely on that later framer to recover events it has already withheld.

| Event | Carrier / action |
| --- | --- |
| `response.created`, `response.in_progress` | Walk their `response` object using §4; no requirement that it contain output. |
| `response.output_item.added` | Typed function/custom names use exact namespace-aware reverse; initial `arguments`/`input` uses its argument lane. Capture item id and argument kind. Other item types retain their existing typed policy. |
| `response.output_item.done` | Walk the typed item using §4. Complete function/custom argument values reset their argument lane; complete message text resets its text lanes. A reasoning item remains untouched, even if its content includes `{type:"text",text:…}`. |
| `response.output_text.delta` | `delta` through the text lane with hold. |
| `response.output_text.done` | Complete `text` reversed without hold, then clear that lane's carry. |
| `response.content_part.added` | Preserve; the cited Chat converter supplies an empty output-text part. |
| `response.content_part.done` | Only an assistant-visible text part belonging to a message receives complete-fragment brand reverse and text-lane reset. Preserve reasoning/non-text parts. |
| `response.function_call_arguments.delta` / `.done` | `delta` with argument-safe hold / complete `arguments` without hold and lane reset. |
| `response.custom_tool_call_input.delta` / `.done` | `delta` with argument-safe hold / complete `input` without hold and lane reset. Keep the custom argument event kind. |
| `response.reasoning_text.*`, `response.reasoning_summary_text.*`, `response.reasoning_summary_part.*` | Preserve. |
| `response.completed`, `response.incomplete` | Walk nested `response` with §4, then apply terminal carry precedence (§5.4). |
| Plugin-visible `response.failed`, `response.error`, `response.done` | Terminal-only; preserve their other fields. |
| Unknown `response.*` type | Preserve; do not treat an unknown suffix as terminal. |
| Root `error` object / `type:"error"` without Responses discriminator | Unknown-body handling, not an invented flush opportunity. |
| Optional `[DONE]` | Preserve framing control. For an already pinned Responses session, existing terminal handling may flush attributable carry before the sentinel; never infer family from the sentinel or depend on its arrival. |

`.done` carries a complete value. Reverse that value without hold and clear the corresponding pending carry so it cannot later be emitted after the authoritative value. This does not promise that every delta-only consumer receives a flushed tail before every `.done`; the complete event is authoritative, as in the existing contract. No additional delta is synthesized solely to duplicate that complete value.

Native Codex → Responses passes `response.custom_tool_call_input.delta` through today. It is in scope, not a future-version gate. Codex can consume it for streamed custom-tool/patch preview; a delta left cloaked while `.done` is restored would make preview and completed content disagree.

### 5.2 Lanes and metadata

Keep the existing per-request stream session and hold algebra. Add the Responses lane prefix:

```
responses:<output_index>:<content_index>          text lane
responses:<output_index>:0\x00args                argument lane
```

- Use event integer indices, not the Chat `laneIndexNum` parser. A dedicated `responsesLaneParts` parser returns output index, content index, argument marker and validity.
- Capture `item_id` from an event, or `item.id` from an item event, for that output item. Text parts of the same item have separate content indices. Never share state between requests.
- Retain the item's type and the argument event kind, `function_call_arguments` versus `custom_tool_call_input`, from typed item/delta events. This is lane metadata, not another session manager or a generic routing layer.
- Only message text lanes receive full prose reverse. Function/custom arguments use the existing argument-safe subset; bare brand words and unrelated executable data stay literal.
- Sort flushes by numeric `(output_index, content_index, isArgs)`, retaining the existing late-arrival tie-break. Existing per-rule carry suffixes remain unchanged.
- A malformed event or missing lane identity must not be mapped onto a guessed index/item. Preserve unsupported data; if carry cannot later be attributed safely, drop it with a debug log at the appropriate cleanup boundary.

### 5.3 Flush encoding

Select a Responses flush encoder from the lane prefix and captured argument kind, not from normalized `openai` alone:

| Lane | Synthesized frame |
| --- | --- |
| Message text | `event: response.output_text.delta` and JSON `type` of the same name, with `item_id`, `output_index`, `content_index` and `delta`. |
| Function arguments | `event: response.function_call_arguments.delta` and matching JSON `type`, with `item_id`, `output_index` and `delta`. |
| Custom input | `event: response.custom_tool_call_input.delta` and matching JSON `type`, with `item_id`, `output_index` and `delta`. |

Terminate each synthesized SSE frame correctly. Do not use the Chat-only `data:` encoder or emit an argument carry as assistant text. Do not synthesize `sequence_number`. Never invent `item_id`; a carry with no captured item id or argument kind is dropped and logged rather than emitted into another item's stream.

### 5.4 Terminal, EOF and cleanup

On an actually intercepted terminal: mark the request's stream disposition clean-terminal, refuse later payload mutations for that request, and decide carry per lane. Process an authoritative terminal output using §4 before resolving its carries.

| Terminal coverage | Carry action |
| --- | --- |
| Lane item id exists in authoritative `response.output[]` | Discard carry; the complete typed item already contains the value. |
| Lane item id is absent from that output, or no output is present | Flush attributable carry before the terminal frame, with the lane's own event kind. |
| No safe item id/kind | Drop and log; never guess. |

One item can cover several text lanes. Match item ids rather than assuming the presence of any `output` array covers all lanes. Preserve the terminal frame's remaining fields.

The cited Chat converter consumes upstream `[DONE]` and emits no downstream sentinel; Gemini conversion also consumes it. Native pass-through and the HTTP framer can preserve a sentinel if supplied. **Responses correctness must not require `[DONE]`.**

The Chat converter can suppress the terminal event altogether: with empty/invalid unfinished tool arguments and absent `finish_reason`, finalization skips the tool and returns without `response.completed`. Do not describe this as a completed response with a partial tool output.

Host errors can also bypass the plugin: the Codex executor turns a failed event into `StreamChunk.Err`, the handler handles that error before payload interception, and the HTTP writer emits the failure afterward. A source function recognizing an error type does not prove that type reaches this plugin.

There is no post-stream emission hook. If no terminal/sentinel is intercepted, emit nothing after the last ordinary frame. At `request.complete`, drop pending carries with one debug line and release all request-owned plan, selector, disposition and lane state. An aborted stream can lose its held tail; do not claim lossless output in that case. Cleanup must run independently of whether a terminal, sentinel or a completed tool item was observed. Preserve the existing clean-terminal and duplicate-request protections.

## 6. Invariants and admission boundaries

1. **Exact tool identity authority.** A transformed tool identity restores only through its request plan; an untransformed namespace/native target receives no reverse entry. Selector restoration uses the recorded name value. Do not infer authority from executed bodies, target-looking names or a different request.
2. **Existing brand semantics.** Brand rewriting remains semantic/canonical, not universally bijective. Preserve the intentional opening-identity replacement, bare `CLAUDE.md → AGENTS.md` convention, approved non-bijective pairs in `CONTEXT.md` §1, existing prose-tool-alias behavior and §7 custom forward-only behavior. This change adds no brand rules or exact custom round-trip promise.
3. **Fail-closed correlation and mutation.** Existing missing/duplicate `RequestID` and alias-plan admission failures remain 503 `tool_cloak_required` with zero upstream attempts. Complete validation precedes body publication; never partially cloak a rejected request.
4. **ProtectedAGY shape gate.** `routeKindProtectedAGY` accepts only `openai-chat` or `anthropic` under its existing structured admission. A Responses or unknown shape fails 503 `omp_cloak_required` before upstream dispatch. Normalized `openai` alone is not sufficient. This also prevents protected total-concealment routes from exposing pass-through namespace children. Explicit OMP non-AGY durable bypass remains unchanged.
5. **WebSocket boundary.** Responses WebSocket/duplex support is out of scope. On the pinned normal entry path, recognize case-insensitive `Upgrade: websocket` plus the `upgrade` token in `Connection` from existing request headers. After mandatory marker/protected controls and existing correlation checks, make the Responses request body a no-op and pin zero mutation for its `RequestID` across response/stream handling. Reuse request lifecycle ownership; do not add a transport configuration/metadata protocol. With no correlated authority, Responses restoration is already a no-op. Ordinary HTTP remains eligible.
6. **Transport evidence is precise.** The pinned ordinary WS path does traverse the common chunk interceptor, and original handshake headers reach the before-auth request. It remains unsupported because duplex/multiple-response terminal semantics are outside this change, not because a bypass was proven. Do not apply HTTP clean-terminal semantics to WS traffic.
7. **Semantic preservation on changed bodies.** Fields outside the specified carriers keep values and types. `safeMarshal` can reorder object keys; no key-order/byte-equality claim applies after mutation. Its existing `SetEscapeHTML(false)` and `json.Number` behavior remains. Byte-for-byte preservation applies to bodies returned without mutation, subject to the explicit request-control/rejection exceptions in §2.
8. **Bounded extension.** No new brand target, keyword set, generic reverse walk, provider registry, state manager, version negotiation or namespace-concealment feature is introduced.

## 7. `custom_mappings` compatibility; automatic inversion deferred

This Responses extension preserves the current brand `custom_mappings` contract. It does not add automatic custom inverse rules or invertibility validation.

Keep the accepted object, string-array and delimited-string forms, normalization, ordered forward application, client scoping, `use_default_keywords` behavior and existing malformed-input errors. Add no new rejection for duplicate custom targets, chains, overlap or guessed invertibility. Tool identity aliases remain owned by `tool_mappings` and the request plan.

`Cursor: Antigravity` remains accepted. When eligible system/machine text is rewritten, its downstream echo follows the existing resolved client's built-in reverse authority: a Codex fixture may restore `Antigravity` to `Codex`, not `Cursor`. A custom target with no separately matching built-in reverse token remains literal.

Unique replacement strings do not prove reversibility. In the ordered array `["SourceAlpha: SourceBeta","SourceBeta: GammaTool"]`, both sources can become `GammaTool`. Preserve that forward behavior and do not invent an inverse source. The same applies to two sources intentionally sharing `GammaTool`.

Automatic custom inversion is a separate deferred enhancement requiring its own specification for ordered composition, boundaries, collisions and request-frozen configuration authority. No dynamic custom inverse or new inverse snapshot is part of this change. This deferral removes the prior proposal's unsound bijection claim while preserving existing clients; it is not an unresolved implementation decision.

## 8. Unsupported-format visibility

For a body that reaches classification and is `unknown`, emit one `debugLog` decision per handler invocation, identifying `SourceFormat` and the untouched-body disposition. `gemini` and `interactions` source wire formats remain unsupported even though a Gemini *upstream translation target* is included in §3.4.

Do not run detection/walkers on an unknown body, and do not reserialize a body returned unchanged. A log does not override marker sanitation, pinned bypass or an applicable admission rejection. No new user-facing error envelope is added for ordinary unsupported traffic.

## 9. Implementation decisions

- Extend the existing handlers/walkers with direction-specific wire-family dispatch. Keep current Chat and Anthropic paths and their tests intact; no reordering that changes their eligibility.
- Use one original Responses source inventory and request plan as mutation authority. Evaluate the two concrete pinned host projections inside existing admission. Preserve per-converter descriptor differences rather than implementing a fictitious universal translator.
- Reuse alias admission/collision lifecycle ownership. Add only the Responses identity/reference checks, selector record, WS no-mutation disposition and ProtectedAGY wire-family gate needed by this contract.
- Extend exact identity walkers for flat/nested declarations, typed output items and namespace context. Extend `reverseAssistantBrandInJSON` for the carriers in §4. Reuse `walkOpenAITextParts`; enforce the containing item-type guard before calling it.
- Streaming adapts the existing framing seam for the three §5.1 callback forms, then adds `reverseBrandResponsesStreamingMap`, the lane parser/prefix, minimal item/kind metadata and the Responses flush encoder. Reuse `applySemanticBrandLane`, `getBrandLane`, `laneReverseTable` and existing static hold algebra. No new parser framework or custom-inverse table change is required.
- Preserve one request's authority until lifecycle cleanup, including after a terminal. Late chunks must not rehydrate a clean-terminal/bypass request into generic restoration.
- Source examples and acceptance expectations define behavior; helper placement and names other than the stated existing seams are ordinary implementation choices. Do not refactor unrelated clients or introduce dependencies to implement these small shape extensions.

## 10. Out of scope

- Native `gemini`/`interactions` client wire support.
- Brand-rule additions, automatic custom inverse generation or new custom configuration rejection.
- Namespace child concealment or a guarantee of total concealment on ordinary Responses routes.
- Responses WebSocket/duplex/multiple-response sessions; their request body is gated off as specified.
- `prompt` template references, `background` workflows, `conversation` and `previous_response_id` continuation.
- Provider-built-in payload transformation and named-selector extensions outside §3.5.
- Reshaping host echoes to resemble the original client request.
- Plugin SDK/host upgrades, runtime converter detection, live deployment/probes or gateway configuration changes in this documentation phase.

## 11. Acceptance criteria

Use the existing black-box `handlePluginCall` lifecycle/HTTP harness with raw request bodies and observable upstream-attempt counts. Do not replace existing tests or weaken their assertions. A new fixture must fail if the targeted guard/walker is removed, and preserve a nearby negative carrier that must stay literal.

Use `X-Cloak-Client: codex` for alias-plan fixtures when detection itself is not the behavior under test, with unique request ids. Include a declared dynamic `mcp__srv__tool` so selector/declaration agreement cannot accidentally pass using only the static table. Compare supported Chat/Anthropic counterparts as regression controls. For unknown-body pass-through fixtures, do not accidentally establish an alias-plan/protected authority whose required rejection would contradict the intended case.

| # | Observable criterion | Required negative/compatibility assertion |
| --- | --- | --- |
| A1 | Coarse flat Responses declarations resolve the same client and transformed identity set as their equivalent Chat fixture; flat, nested, absent-type function and custom carriers enter the intended plan. | Existing Chat/Anthropic bodies keep their existing behavior. Assert the current flat-declaration unsupported-admission gap is closed, not bypassed by skipping admission. |
| A2 | Rewrite `instructions` and system/developer message text under existing brand and declared-alias policy. | Plain-string `input`, typed user brand/tool words and request call `arguments`/`input` remain literal; a recognized reminder/tool-result span still follows existing policy. Schema structural values and reasoning are preserved. |
| A3 | Flat alias cap/no-op requirement and namespace policy hold for function/custom declarations. | Over-cap alias returns 503 and zero attempts. Nonempty namespace children, including `mcp__*`, already-qualified and equals-namespace names, stay unchanged with no reverse authority. Effective flat-name precedence wins over a different ignored `function.name`. Include empty namespace and the Gemini `children` inventory case. |
| A4 | `additional_tools` declarations participate in detection, planning and rewriting exactly as supported top-level declarations. | Same-source repeats across both channels, including differing schemas/descriptions, are accepted with one alias and the host's original winning definition. No item is treated as conversation text. |
| A5 | All §3.5 supported selector shapes follow the declared static/shared/dynamic alias; history uses the same owner decision. | Test qualified history without `namespace`; flat-vs-namespace local-name selection; alias shadowing of bare `run_command` rejects while explicit namespace succeeds; two namespace `exec` children do not create a fallback alias. Strings/pass-through references stay unchanged. Conflicting name fields and unsupported structured selectors cannot leave stale references after renaming. |
| A6 | Non-stream reversal restores transformed call/declaration identities and eligible prose/arguments in both converted-Chat echoes and original-Responses Gemini/AGY echoes. | `instructions` is optional, not universally absent. Reasoning, literal bare brands in arguments, ids/types and native target-only tool names remain unchanged. A namespaced `run_command` is not flat-reversed. |
| A7 | Split text tokens reassemble/reverse; an uncovered held text tail flushes with Responses event/type, correct indices and captured item id. | No Chat/Anthropic flush encoding; no guessed missing id/index; adjacent punctuation/path boundaries remain correct. |
| A8 | Both function-arguments and custom-input split-token streams use argument-safe reversal and the correct delta event kind. Include a custom patch/path split across `.gem` / `ini/AGENTS.md` and compare with its completed input. Replay custom added/delta/done/terminal through the actual native callbacks: separate delimiter-free `event:`/`data:` lines and data-only lines, with no empty callback or `[DONE]`; all complete events must be delivered. | Repeat the same logical stream as raw JSON and full/fractured SSE to preserve the existing paths. A genuinely split JSON string whose continuation starts with literal `data:` must not acquire an inserted newline or become a new event. Neither argument kind becomes text or the other kind; bare brands, JSON keys and unrelated literals remain unchanged. |
| A9 | Complete `.done` values reset the appropriate lanes; terminal nested output and supported echoes reverse under §4. | No covered carry re-emission. Reasoning output_item.done with `content:[{type:"text",text:"Antigravity .gemini/AGENTS.md"}]` stays literal, as do reasoning deltas and unrelated parts. Namespace output identities remain intact across added/done/terminal carriers. |
| A10 | Two concurrent requests, distinct output items and two content indices of one item keep separate state. | No carry, selector record, alias authority or terminal disposition leaks across requests; test argument/text lane separation and late chunks after terminal/cleanup. |
| A11 | ProtectedAGY rejects Responses and unknown shapes with 503 `omp_cloak_required` and zero attempts. | Existing protected Chat/Anthropic behavior and explicit OMP non-AGY durable bypass are unchanged. The WS no-op cannot bypass a protected rejection. Ordinary HTTP remains eligible while a Responses WS-upgrade request pins zero mutation through response/stream. |
| A12 | Existing custom mapping forms, scoping/default toggle and forward behavior remain compatible. `Cursor: Antigravity` still works; Codex reverse follows built-in canonical authority. | Ordered `SourceAlpha → SourceBeta → GammaTool` and duplicate `GammaTool` targets remain accepted without a fabricated inverse/new invalid_config. Existing malformed input still fails and leaves the prior config live. No custom inverse snapshot behavior is claimed. |
| A13 | Ordinary unknown-format/shape traffic remains body-byte-identical with one decision log and no body detection/walker. | Header consumption still occurs; applicable protected/alias-plan failures and pinned bypass are not replaced with an unconditional empty envelope. Exercise root `error` / `type:"error"` as unknown. |
| A14 | A stream with no terminal and no sentinel, including an unfinished tool call, emits nothing after its final ordinary frame; `request.complete` drops/logs carry and clears state. | A terminal actually received flushes only uncovered attributable lanes. Host-error cleanup works without pretending the later HTTP-written failure traversed interception. An optional native sentinel is preserved; absence of it never prevents cleanup. |
| A15 | Distinct-owner collisions/owner drift caused by aliasing fail 503 before dispatch in each pinned projection. | Same-source repetition succeeds; unchanged namespace-only collisions remain pass-through. Cover rewritten-vs-native flat target, capped Chat collision, Gemini sanitized-name ownership, mixed flat/namespace winner precedence and target shadowing. Do not require the two original converters to agree. |
| A16 | Restore the recorded selector name in chat-shaped non-stream and Responses-shaped terminal/original-request echoes. | Preserve each echo's shape and non-name fields. A selector not rewritten is unchanged even if its literal matches an active reverse key. Concurrent requests cannot share selector records. |
| A17 | `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"Antigravity"}]}]}` is classified/reversed as Responses; `{"object":"response"}` is a valid no-carrier response. | Do not mistake response `object` for request shape. Equivalent Chat `choices` remains Chat; Anthropic classification remains format-directed. |
| A18 | Both-discriminator fixtures within one direction classify unknown: request `messages`+`input`; response `choices`+`output`; stream `choices`+`type:"response.completed"`. | Apply §2's admission exceptions explicitly. A normal terminal frame is classified once at its root; nested `object:"response"`/`output` and optional echoed `instructions` never cause reclassification. Empty arrays/strings and neither-discriminator cases are covered. |

A5/A15 need fixtures derived from the pinned converter behaviors, not tests of a newly invented resolver against itself. Record the expected winning source declaration and emitted/reference name for both projections, including a case where their original outcomes differ. Existing host tests can supply the oracle inputs; no host import or new production dependency is required.

Implementation regression gates: `go test ./...`, `go vet ./...` and the existing CGO/shared-library build procedure must pass. Keep the existing custom-mapping, request-scoped authority, OMP/admission and stream boundary suites. Live gateway acceptance and rollout checks (§12) remain separate from the offline test suite.

## 12. Evidence gates and rollout

No unresolved gate below leaves an algorithm or security disposition for the implementer to invent. Source-resolved behavior is pinned; real deployment observations remain explicitly unrun.

| # | Status | Evidence / remaining action |
| --- | --- | --- |
| U1 | **Rollout verification — NOT RUN here.** | Capture one target-client request and record actual host revision, `SourceFormat` and shape. The historical opencodex Chat route does not establish today's deployment. Verify this compatibility profile before enabling/deploying the Responses change; it does not block offline implementation. |
| U2 | **RESOLVED FROM SOURCE.** | Echo source depends on converter: Chat non-stream uses converted request; Gemini/AGY non-stream and terminal use original Responses request; Chat terminal prefers original too (§4). Both echo shapes are required. Recheck on a host translator change. |
| U3 | **SOURCE PATH RESOLVED; WS CAPABILITY DEFERRED/OFF.** | Normal pinned WS enters the shared interceptor, and handshake headers are available for §6's concrete no-op gate. Duplex may contain multiple responses/recoverable errors. No live WS capture is claimed, and this spec does not enable WS support. |
| U4 | **RESOLVED FROM SOURCE; IN SCOPE.** | Native Codex Responses conversion passes custom-input deltas through today. Implement/test the custom argument lane; do not defer it because the Chat converter emits only custom `.done`. |
| U5 | **SOURCE RULES RESOLVED; REAL-CLIENT CAPTURE NOT RUN.** | The converters accept namespaced and namespace-less history; §3.4 covers both without assuming the sibling field always survives. A real multi-agent request remains a rollout observation, not a missing design choice. |
| U6 | **RESOLVED FOR THE HOST PIN.** | Chat's cap is 64 bytes; qualification/disambiguation/index and Gemini's separate sanitizer/winner rules are source-backed. Guard fixtures pin both behaviors. Re-read on host translator upgrades; changing a plugin SDK version alone neither proves nor updates this evidence. |

A future isolated rollout check follows the repository verification runbook: verify the running binary/host revision, use a separate local acceptance instance, capture only the necessary field/event observations, then restore debug-off operation. `CPA_FILTER_DEBUG` is cached at process initialization; enabling/disabling it requires the runbook's recreate procedure, not a live environment toggle. Do not commit full bodies, credentials or debug logs. This document does not execute that procedure or authorize production changes.

## 13. Source anchors

All `.ref/CLIProxyAPI` paths below are relative to the host pin in the header. Plugin `main.go` references are the baseline. These anchors support the contract; comments or a function's name alone are not proof of its call path.

| Claim | Exact source entry points |
| --- | --- |
| SDK dependency and actual plugin imports | `go.mod:5`; `main.go:59` |
| Raw before-auth request and no selected ToFormat | `sdk/api/handlers/handlers_execution.go:67`; `sdk/api/handlers/handlers_interceptors.go:469` |
| Format values / translator original-vs-converted parameters | `sdk/translator/formats.go:4`; `sdk/translator/types.go:14` |
| Existing marker/protected/bypass ordering and alias-plan entry | `main.go:1162`; `main.go:1189`; `main.go:1243` |
| Existing plan-fed cloak, brand and declared-prose authority | `main.go:1270`; `main.go:1277`; `main.go:1283` |
| Existing collector / readers / request plan | `main.go:398`; `main.go:1640`; `main.go:3581`; `main.go:7474` |
| Existing distinct-owner collisions and exact reverse entries | `main.go:3717`; `main.go:3730` |
| Chat declaration order, namespace children, disambiguation | `internal/translator/openai/openai/responses/openai_openai-responses_tools.go:35`; `:78`; `:126` |
| Effective declaration name / Chat cap and qualification | Same file `:244`; `:333`; `:342`; `:366` |
| Chat winner, reference order and output identity | `internal/translator/openai/openai/responses/responses_tool_index.go:35`; `:62`; `:69`; `:94` |
| Chat history and selector use the index | `internal/translator/openai/openai/responses/openai_openai-responses_request.go:323`; `:479` |
| Gemini descriptor/winner/sanitize/map/selector rules | `internal/util/responses_tools.go:117`; `:177`; `:199`; `:253`; `:343`; `:352`; `internal/util/util.go:24` |
| Gemini request actually uses those rules | `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go:34`; `:55`; `:82` |
| Namespace output fields are explicit | `internal/translator/common/responses.go:11` |
| Chat complete output and echoes | `internal/translator/openai/openai/responses/openai_openai-responses_response.go:85`; `:198`; `:288`; `:653`; `:823`; `:871` |
| Chat item/text/argument complete events | Same file `:372`; `:500`; `:594`; `:616` |
| Chat unfinished tool / suppressed terminal | Same file `:568`; `:637` |
| AGY delegates to Gemini Responses reversal | `internal/translator/antigravity/openai/responses/antigravity_openai-responses_response.go:18` |
| Gemini original-request echo authority | `internal/translator/gemini/openai/responses/gemini_openai-responses_response.go:116`; `:1238`; `:1279`; `:1427`; `:1470` |
| Native Codex Responses registration / pass-through | `internal/translator/codex/openai/responses/init.go:9`; `codex_openai-responses_response.go:15` |
| Native ordinary payload reaches chunk interception | `internal/runtime/executor/codex_executor_stream.go:426`; `sdk/api/handlers/handlers_stream.go:442` |
| Native line callbacks differ from baseline SSE buffering | `internal/runtime/executor/codex_executor_stream.go:147`; `:357`; `sdk/api/handlers/handlers_stream.go:712`; `main.go:4229`; `main.go:4432` |
| Existing host framing oracle and both callback fixtures | `sdk/api/handlers/openai/openai_responses_handlers.go:64`; `openai_responses_handlers_stream_test.go:223`; `:267` |
| Host failure bypasses payload interception | `internal/runtime/executor/codex_executor_stream.go:371`; `sdk/api/handlers/handlers_stream.go:700`; `sdk/api/handlers/openai/openai_responses_handlers.go:1020` |
| HTTP framing can preserve a native sentinel / recognizes failure types | `sdk/api/handlers/openai/openai_responses_handlers.go:157`; `:253` |
| WS enters shared execution / duplex differs | `sdk/api/handlers/openai/openai_responses_websocket.go:691`; `:733`; `openai_responses_websocket_forward.go:153` |
| Original handshake headers reach before-auth hook | `sdk/api/handlers/handlers.go:540`; `handlers_stream.go:338`; `model_execution.go:233`; `handlers_context.go:152`; `handlers_interceptors.go:474` |
| Exact tool reversal / brand carrier guards | `main.go:1784`; `main.go:1810`; `main.go:1877`; `main.go:1903`; `main.go:1914`; `main.go:4459` |
| Existing flush encoding and lane policy | `main.go:2492`; `main.go:2745`; `main.go:7102` |
| Custom parsing/normalization/ordered rewriting and separate tool config | `main.go:5453`; `main.go:5460`; `main.go:5892`; `main.go:5950`; `main.go:6584` |
| Existing custom compatibility fixture | `plugin_test.go:92`; `plugin_test.go:116` |
| Codex custom delta consumer / reasoning item content | `.ref/codex/codex-rs/codex-api/src/sse/responses.rs:366`; `.ref/codex/codex-rs/protocol/src/models.rs:1048`; `:1989` |
| Debug setting is initialization-scoped | `AGENTS.md:153`; `AGENTS.md:263` |

## 14. Change control

The 2026-11-08 amendment supersedes the previous content of §§1–13: it corrects the SDK/host evidence pin, resolves original-owner and duplicate-declaration handling, accounts for both actual lowering paths, restores selector values without reshaping, includes native custom-input deltas and their actual callback framing, clarifies terminal/transport boundaries, and replaces the independent custom-inverse proposal with compatibility-only scope. The current sections are the normative contract; earlier review conclusions and retracted draft claims are not evidence.

The offline implementation in `main.go`, `responses_surface.go`, and `issue53_responses_test.go` implements this contract while preserving `CONTEXT.md` and ADR 0005. Live deployment and gateway configuration changes remain separate rollout work (§12). Later amendments must be dated and identify the sections they supersede.
