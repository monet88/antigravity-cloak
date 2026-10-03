# Changelog

All notable changes to `antigravity-cloak` are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Current releases use semantic version tags; the one-off `v.26.09.06` CalVer release is retained below as historical release metadata.

## [Unreleased]

### Changed
- **Brand rewriting is scoped per client.** The built-in forward tables are declared per client (`claudeCodeBrandMappings`, `codexBrandMappings`, `ompBrandMappings`, registered in `brandMappingsByClient`). `claude_code` and `codex` register their reverse tables (`claudeCodeReverseBrandMappings`, `codexReverseBrandMappings`) in `reverseBrandMappingsByClient`, while Oh My Pi's reverse authority is the protected `ompProtectedReverseTable`, which `brandReverseTableFor` selects for that client. The resolved client selects exactly one. Every brand target a client's forward pass produces therefore inverts back to that same client (the one approved one-way filename exception is named under the reversible-rule entry below): a bare `Antigravity` becomes `Claude` for `claude_code`, `Codex` for `codex`, and `omp` for `oh_my_pi`. Previously only the Oh My Pi pair was reversed, so a model that shortened a cloaked token to bare `Antigravity` leaked that word to the other clients.
- Oh My Pi's opening sentence is now rewritten whole. The harness ships `You are omp's trusted coding assistant.`, and the bare-alias pass alone turned it into `You are Antigravity's trusted coding assistant.` - a sentence naming no product and no vendor. A dedicated `ompIdentityLines` rule replaces it with the Antigravity identity line, on both the protected route and the plain OMP branch. Ordering matters and is now pinned by a test: `mandatoryProtectedOMPAliases` swaps the `omp` for a sentinel, so the sentence rule has to run first or it can never match.
- Oh My Pi gained one reversible path pair: the user's **global** Claude memory `~/.claude/CLAUDE.md` -> `~/.gemini/AGENTS.md` in both directions, covering POSIX and Windows spellings. Its own discovery reads that global file (`discovery/claude.ts:65-69,163-188`) and it exists on disk while its root context file is the neutral `AGENTS.md` (`discovery/agents-md.ts:21`), so the file name is a real operational identifier on this route. The scope is the file and never the `.claude` directory - a blanket `.claude` -> `.gemini` would land on the same target as `.omp` -> `.gemini`, and one target with two sources cannot be reversed. The pair inverts unambiguously because `AGENTS.md` directly after `.gemini/` is a shape no `.omp` path produces. Every other competitor directory (`.codex`, `.cursor`, `.windsurf`, `.vscode`, `.opencode`, `.copilot`) and the rest of `.claude` stay verbatim.
- Removed the competitor and general-agent brand rules (`Cursor`, `Windsurf`, `Cline`, `Devin`, `OpenCode`, `Trail`, and the rest). They belonged to no supported client, and an unowned rule mapping onto `Antigravity` left the reverse with no way to tell which source had produced it. The bare `CLAUDE.md` -> `AGENTS.md` brand rule is now `claude_code` only. It stays there because that table's bare `Claude` rule would otherwise consume the vendor prefix inside the file name and corrupt its case on the way back; `codex` and `oh_my_pi` carry no such rule, so dropping it from their tables changes no behaviour and stops them renaming a file the harness really does load.
- `rewriteProtectedBrandText` now applies client scoping; it walked the mapping tables directly and so ignored `rewriteMapping.Client` entirely.
- Consolidated the two streaming brand-reverse loops into one. `reverseBrandSSE` and its non-Oh-My-Pi twin were the same loop - same event splitting, same terminal-flush discipline, same per-event rewrite - differing only in the applier and the lane flusher, and each carried its own near-identical single-event wrapper. `brandReverseFuncs` now selects both once per session and the shared wrapper is `rewriteSSEEventData`. Behaviour is unchanged: a token split across events is still held out of both and emitted once, reversed, on the event that completes it, and `content_block_stop` / `message_stop` still flush one lane / all lanes (PR #46).
- Corrected the `CONTEXT.md` reverse-restoration contract, which the per-client tables had outgrown: the scope is assistant text and every tool-argument carrier - the Anthropic streamed `input_json_delta.partial_json`, the OpenAI streamed `choices[].delta.tool_calls[].function.arguments`, and their non-stream twins `content[].input` and `choices[].message.tool_calls[].function.arguments` - each resolved with the resolved client's own table. The stale claim that Claude Code brand restoration is one-directional is replaced by its actual state: implemented on both the response and stream paths, still unattested against real traffic because the 2026-09-27 completion attempt was blocked upstream by HTTP 429.
- **Casing is preserved by a mechanism, not by rule order.** A rule whose match is a single brand word and whose replacement is a plain word (`Codex` -> `Antigravity`, `Claude` -> `Antigravity`, `OpenAI` -> `Google Deepmind`) copies the matched casing onto the replacement, so `Codex`/`codex`/`CODEX` become `Antigravity`/`antigravity`/`ANTIGRAVITY` and the reverse restores the same three spellings. Previously this was three overlapping case-insensitive rules whose relative order decided the output, which is why the vendor word could come back lowercased. Composed and operational tokens (tool names, SDK names, paths, model IDs, `claude.ai`, the Oh My Pi sentinel) are unaffected: they carry a separator and never take the matched casing.
- **Codex's brand surface is declared from the client's own prose.** Its shipped opening identity `You are Codex, an agent based on GPT-6.` is now rewritten whole to the Antigravity identity line, alongside the Claude Code and Oh My Pi identities. The `CONTEXT.md` and doc claim that Codex carries no vendor token was wrong: the catalog ships `GPT-6` in that same sentence, and longer identity prose names `OpenAI`. Codex therefore has the residual prose pairs `Codex` <-> `Antigravity`, `OpenAI` <-> `Google Deepmind` and `GPT-6` <-> `Gemini 3`, each reversible. Its operational identifiers (`$CODEX_HOME`, `codex_apps`, `.codex/...`) are untouched.
- **Every built-in forward rule is reversible, or it is gone - with two approved non-bijective pairs and one approved one-way convention.** Removed the four one-way prose model-ID rules: `claude-fable-5-1` and `claude-opus-5-5` both landed on `gemini-3.1-pro-low` (two sources, one target, so no reverse could tell them apart), and `claude-sonnet-5` -> `gemini-3.8-flash` / `claude-haiku-4-5-20251001` -> `gemini-3.5-flash-lite` have no usable reverse either - inverting one of those routes would hand a client that legitimately received it an Antigravity spelling it never wrote. A prose model ID is not worth a one-way rewrite that leaves the request looking clean while the client is handed a model name it cannot resolve. The PR #46 body still describes the earlier decision to keep these four rows and needs correcting remotely. Removed the special phrase `Anthropic's official CLI` -> `Google's official CLI` for the same reason: the whole identity line already handles the canonical sentence and the accepted `Google Deepmind` vendor vocabulary covers residual Anthropic prose. Removed the speculative `Codex.google` and `omp.google` source domains from the repair tables - no such domain exists in the accepted contract; `antigravity.google` is a real native Antigravity host. Two non-bijective canonicalization pairs are approved. The Claude SDK name: `Anthropic SDK` and `Claude Agent SDK` both forward to `Antigravity SDK`, and the single reverse rule resolves `Antigravity SDK` back to `Anthropic SDK`, so a client that wrote the second name reads the first back. The Workflow name: `Workflow` and `Workflows` both forward to `teamwork_preview_layer`, and the single reverse rule resolves `teamwork_preview_layer` back to the singular `Workflow` (the plural canonicalizes to the singular; grammar is not repaired in either direction). No second reversible target is invented for either pair. One convention is deliberately one-way and stays that way: the bare `CLAUDE.md` -> `AGENTS.md` brand rule owned by `claude_code`. `AGENTS.md` is the neutral context filename every client already reads, so it is never reverse-restored as a bare filename - inverting it would hand a `codex` or `oh_my_pi` client a `CLAUDE.md` it never wrote - while the path-qualified context groups remain fully reversed. The rule exists only to stop `claude_code`'s bare `Claude` rule from consuming the vendor prefix inside the file name. This is a deliberate convention accepted by Issue #49, not a regression, and `docs/verification-checklist.md` records it as intentional.
- **One walker for an OpenAI `content` value.** `reverseBrandInOpenAIContent` and `applyOpenAIProseContent` each carried their own copy of the same three-shape switch (plain string, array of strings or text-part objects, single text-part object), so a fix to one could silently miss the other. Both now call `walkOpenAITextParts`, which owns the shape handling and reports whether a string changed; each caller supplies only its own rewrite. The supported shapes, the in-place mutation and the changed flag are unchanged, and the request-scoped tool-alias pass was moved onto the same seam (`rewriteDescriptiveText`) so the schema's descriptive fields cannot drift from the declaration's.

### Fixed
- **A present, non-standard `index` keeps its own lane identity through the `laneKeyWithIndex` consolidation.** The Anthropic reader had always keyed such an event by its value (`default: fmt.Sprintf("anthropic:%v", v)`); the shared helper replaced that with a fall-through to lane 0, so an event whose `index` arrived as anything but a JSON number (a string `"1"`, for instance) merged onto `anthropic:0`, and a carry held for one block could be flushed through another. The arm is restored in the helper, so a present index is keyed by its value and only an absent one defaults to lane 0. Numeric indices (`json.Number`, `float64`, `int`) are unchanged, and a non-integral number - which has never parsed - still defaults.
- **An OpenAI choice's terminal continuation is recognised in every content shape the applier handles.** The pre-flush skip set was computed from a string-only `content` check, so a choice whose `delta.content` arrived as a content-parts array (`[{"type":"text","text":"igravity"}]`, or a bare string element) or as a single text-part object looked like it had no continuation at all: the held partial was drained raw ahead of the `finish_reason` and the remainder followed it, so `Ant` + `igravity` reached the client as the cloaked `Antigravity` instead of its own `omp`/`Claude`. Both appliers now share one shape test (`openAIContentAppendsProse`) and one content mutator (`applyOpenAIProseContent`), and the decision about which lanes the terminal event still feeds is that same test - so the shape a lane is judged to continue is exactly the shape the applier appends to. The shared (non-Oh-My-Pi) applier also now reverses the array and object content shapes it previously passed through untouched.
- **A finished content block's held argument token is flushed with the block, not after it.** The filtered terminal flush selected lanes by exact key, so `content_block_stop` for `anthropic:1` did not select the argument lane keyed beneath it (`anthropic:1\x00args`) and the recovered token reached the client at `message_stop`, after the block was closed - a client that finalizes the block at the stop reads a truncated tool call, which is what Oh My Pi's protected route was doing on the primary live path. The selector is now parent-or-child, the same match `flushBrandStandalone` already made on the non-stream path.
- **An OpenAI choice's held carry is flushed at its own `finish_reason`, not at `[DONE]`.** The SSE path held a finished choice's carry until the end of the stream, so the recovered prose or tool-argument delta arrived after the event that finalizes the choice - out of spec, and dropped by a client that acts on `finish_reason`. A non-null `finish_reason` now flushes that choice's own prose and tool-argument lanes into the same emitted frame, ahead of the event carrying it; choices still streaming keep their carries (`n > 1` finishing one does not flush another), and `[DONE]` still flushes whatever remains. A token the terminal event's *own* delta ends on is resolved inside that event as well (`streamSession.terminalChoices`): no further bytes can arrive for the choice, so its lanes do not hold a trailing partial past the `finish_reason` that closed it. The hold itself is unchanged everywhere else - the skip is per event and per finished choice, so the chunk-boundary semantics of Issue #48 still apply to every streaming choice.
- **A token split across a choice's own `finish_reason` event resolves inside that event, not raw in front of it.** The terminal pre-flush drained the finishing choice's lanes before the event was applied, so a carry the SAME event continues was emitted unresolved and its lane emptied: a model streaming `Ant` then `igravity` with `stop` handed the client `Antigravity` - the cloaked spelling - where the request had cloaked `omp`/`Claude`, and `{"path":"/home/u/.gemini` followed by `/agent"}` handed an ordinary client a `.gemini/agent` path that does not exist on its machine. A lane the terminal event itself appends to - the choice's prose lane and each streamed tool call's argument lane - is now held back from the pre-flush and resolved inline under the terminal/no-hold mark, in the field its text arrived in. Lanes the event does not touch are still flushed ahead of `finish_reason`, and other choices are undisturbed.
- **`finish_reason` is detected on the decoded key, not on the raw bytes.** The OpenAI terminal scan fast-pathed on the literal `finish_reason`, so valid JSON spelling the underscore as a JSON escape (`finish\u005freason`) never marked the choice terminal: its carry stayed held past its own `finish_reason` and was flushed at `[DONE]`, behind the event a client finalizes the choice on. The gate now also scans any payload containing a backslash - the same backstop `sseAnthropicTerminalKind` keeps for its markers - and detection itself stays on the parsed key. Malformed and multi-`data:` events behave as before.
- Codex's second shipped identity sentence is now a whole-sentence rewrite: `You are Codex, an OpenAI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows.` Residual substitution would have produced `an Google Deepmind general-purpose agentic assistant` - ungrammatical, and a sentence no client ever wrote. The GPT-6 identity line and both short-sentence paths keep working, on every turn of a continuation.
- The bare Oh My Pi reverse rule `.gemini` -> `.omp` was only half a boundary. `WholeSegment` enforces the left side; the generic right boundary accepts any non-word byte, so `.gemini-backup` and `.gemini.foo` were handed back as `.omp-backup` and `.omp.foo` - spellings the forward dot-segment remap deliberately preserves and never produces. The rule now carries `SegmentEnd` (end of string or a path separator), the exact mirror of the forward guard `isDotPrefixedPathSegment`, and it is chunk-safe: a complete word-final match is held while its right boundary is unproven, so a stream fragment cannot resolve it early. `.gemini/`, `.gemini\` and the global-memory pair are unaffected.
- **Path rules match whole segments, not suffixes of larger elements.** `.claude/` and `.codex/` are rewritten only when they are the whole element: `foo.claude/CLAUDE.md` and `foo.codex/config.toml` are left alone. The generic word-boundary test cannot see this - the element begins with a dot, which is not a word byte - so every path rule carries an explicit whole-segment boundary. Valid absolute, tilde and project paths, both delimiters (`/`, `\`), and the JSON-escaped Windows forms are unaffected.
- **The JSON-escaped Windows file spellings of the context groups are declared.** A path inside a tool call's arguments arrives as JSON text with every backslash doubled, and the file rule only existed in the plain spelling, so the directory rule claimed the directory and left the file name behind: `C:\\Users\\dev\\.gemini\\GEMINI.md` reached the client as the mixed `.claude\\GEMINI.md`, and Oh My Pi's global Claude memory as the mixed `.omp\\AGENTS.md`. Both escaped forms are now part of their group and invert exactly, on the response path and on both streamed tool-argument carriers.
- **A standalone OpenAI stream with `n > 1` was freed before every choice arrived.** Completion counted every open lane, so the tool-call argument lane a choice opens made `n = 2` look satisfied from choice 0 alone and the session was deleted before choice 1 streamed anything - its brand carries were then unflushed. Completion now counts root choice lanes: a child argument lane can neither end the stream nor keep it open after its own choice is finished.
- **A host-like token at the start of a request no longer has its scheme mis-detected.** The backward check for `:` was wrong for `https://`, where the slashes sit between the colon and the host, so a URL that had no immediately preceding slash was not recognised and the bare brand inside it was rewritten. The scanner now recognises the scheme without depending on that preceding byte. The tests cover the host and the escaped path together, streamed and non-streamed.
- The `goal`, `yield` and `wait` tools were missing from the Oh My Pi shared alias table. They are declared only while `/goal`, `/goal` variants or `/loop` is active, so they were invisible in a default session and silently took the deterministic `wp_ext_<hash>` fallback - live traffic showed `wp_ext_63f44033c2aa0953` for `goal` and `wp_ext_061bef0f1c6ccd0b` for `wait`. Every other family in that table (`vibe_*`, autoresearch, memory) is named rather than hashed, and `codex` has mapped its own `wait` to `wp_wait` all along, so this was an omission and not a decision. The fallback is unchanged and still resolves any tool a future Oh My Pi release declares.
- **A bare vendor word inside a URL or path is rewritten only as a literal dot-prefixed directory segment.** `/omp/`, `/claude/`, `/codex/`, `\omp\`, `https://omp.ai/...` and a directory name that merely contains the brand word are left byte-for-byte alone, in both directions. Live acceptance caught the real damage: this repository lives at `F:/CodeBase/antigravity-cloak/`, the path came from the user's own typed prompt so the forward pass correctly left it literal, the model echoed it back, and the case-insensitive response reverse rewrote it to `F:/CodeBase/omp-cloak/`, so `read` failed with "Path not found". A forward-only guard was not enough: the reverse pass cannot know which tokens the forward pass introduced, the same one-way-authority class as the fixes below. Composed entries such as `claude.ai` and `Antigravity SDK` are exempt and keep cloaking anywhere, and prose is unaffected. This reverses the earlier decision that non-dot delimiters should mask to `Antigravity`.
- Synthetic Anthropic carry-flush events were written as a bare `data:` frame with no `event:` line, in both the shared encoder and the standalone/composite emission site. A client that dispatches on the SSE event name - the way the Anthropic Messages stream itself frames every event - never saw the recovered text at all. Both sites now emit a complete `event: content_block_delta` + `data:` frame, and the shared SSE test helper parses event names, failing when a name disagrees with the payload's own `type` (Issue #47).
- Two held tokens from the same content block were flushed in reverse-table declaration order rather than the order the model produced them, because same-index lanes were tie-broken by key string. A block holding `Google Deepmind` before `GEMINI.md` reached the client transposed. Lanes now carry the order they were opened (`brandLane.seq`) and `sortedCarryKeys` tie-breaks on that (Issue #48).
- `findHoldLenWithBoundary` retained a suffix that could no longer become a boundary-valid match, deferring the client's text for nothing. A candidate is now held only while it can still grow into one (`holdIsLive`): a match already complete and ending in a non-word byte is boundary-valid at end of text and is emitted immediately (Issue #48).
- A client's home directory was remapped forward to `.gemini` and never restored, so a model that read or wrote `~/.claude`, `~/.codex` or `~/.omp` handed the client a directory that does not exist on its machine. The remap is now client-scoped in both directions - `.gemini/` and `.gemini\` invert to each client's own spelling, and the Oh My Pi protected lane owns its home-directory rule in the same single carry as the `Antigravity -> omp` pair. The scope is now the client home directory itself, matched as a whole segment at any position and in both directions, so it covers every spelling a client actually emits. **This is not a preference: live acceptance measured 114 absolute `C:\Users\<name>\.claude\...` paths per request reaching the model uncloaked** - `transcripts` (97), `plugins` (7), the instruction file (6), `lsp-shims` (3), `projects` (1) - because Claude Code on Windows puts absolute paths in its system context and never uses the `~/` or `./` form. An earlier narrowing to a home spelling passed the whole unit suite while leaking all 114, which is why the rules are now asserted against observed traffic rather than against documentation. The file name travels with the directory: `CLAUDE.md` -> `GEMINI.md` for `claude_code`, unchanged for `codex`, whose `AGENTS.md` is already the neutral name Codex reads. The cost is that a `.claude` belonging to a different project is rewritten too, which the plugin cannot distinguish; that is one path the model may wander into, against 114 per request. The bare `GEMINI.md` reverse rule stays deleted: the forward pass always rewrites the directory with the file name, so matching the name alone inverted a path that never existed, and that drift is what collapsed a README edit into a no-op during the earlier live acceptance. The bare `GEMINI.md` and bare wire-form reverse rules were removed for the same reason - the forward pass no longer emits either, so keeping them inverted a path that never existed; `TestContextGroupsAreExactInverses` now holds the two halves of every group together for Codex, per `codex-rs/core/src/agents_md.rs`, the `AGENTS.md`, `AGENTS.override.md` and `skills/` entries under `CODEX_HOME`, which defaults to `~/.codex` but may be set to `./.codex` for a per-repo profile and keeps its file names because `AGENTS.md` is already the neutral name Codex reads; and Oh My Pi's whole home directory, whose blanket dot-segment rule already covered it. A plain `.claude` or `.codex` directory such as `settings.json` is an operational identifier and is left byte-for-byte in both directions. The two directions are now symmetric entry for entry, which is the fix: a forward rule with no matching reverse is what let live acceptance catch Claude Code writing `.claude` over `.gemini` in a README, where the remap collapsed an Edit's old and new strings into identical bytes and the edit silently became a no-op. The blanket `.omp` preserve policy is gone; `.omp` is an operational identifier like any other (Issue #49).
- The cloaked official domain `antigravity.google` had no reverse rule, so a client that received it got a link that does not exist, and the bare `Antigravity` rule matched inside it. The `claude_code` reverse table now carries the domain ahead of the bare brand word (Issue #49).
- The tool-schema walk still descended into the subtree of every literal-bearing keyword, rewriting a `const`, `enum`, `default`, `example` or `examples` value while the property carrying it kept the client's spelling - a schema no arguments could satisfy. Those subtrees are now never descended into; real nested schema traversal under `properties`, `items`, `$defs`, `allOf` and the other schema keywords is unchanged (Issue #50). The skip is a schema-keyword skip, not a key-spelling skip: inside a dictionary-of-schemas container (`properties`, `patternProperties`, `$defs`, `definitions`, `dependentSchemas`) the keys are entry names, so a property literally called `default` still has its own subschema walked.
- The OpenAI Chat Completions wire streams tool-call arguments as `choices[].delta.tool_calls[].function.arguments`, the carrier Anthropic calls `partial_json`, and that carrier was not reversed at all: a `.gemini` path or a cloaked URL written by the model was persisted to the user's disk. Each streamed tool call now owns its own brand lane and is reversed exactly like the Anthropic fragment (Issue #51).
- **A tool-argument carrier restores only the operational rules.** Both streamed carriers and their non-stream twins were resolved with the client's whole reverse table, so a prose rule ran over data the client executes: the model echoed the user's own typed `cd antigravity-cloak && go test ./...` back out of a tool call and the client received `cd Claude-cloak`, a directory that does not exist in that repository. A mapping can now declare itself argument-safe (`rewriteMapping.ArgumentSafe`); the operational identifiers do - paths, `antigravity.google`, the SDK and skill names - and the prose rules (the identity line, the vendor word, the bare brand, and the client's capability name `teamwork_preview_layer` -> `Workflow`) do not. Assistant prose keeps the client's whole table, so nothing else changes.
- **The `teamwork_preview_layer` -> `Workflow` inverse is prose-only.** It was declared argument-safe, so a literal the client wrote itself came back rewritten: `echo teamwork_preview_layer > out.txt` in a tool call reached the client as `echo Workflow > out.txt`. The forward pass writes that name into prompt prose and never into a tool argument, so the argument carriers no longer apply it - the literal survives byte-for-byte, streamed and non-streamed, on both wire formats - while a mention in assistant prose still restores. The tool identity itself is untouched: it round-trips through the alias plan's exact authority.
- The non-stream response path reversed assistant text only, so the same tool-call arguments arrived cloaked in a finished response while the streamed form was reversed. `content[].input` and `choices[].message.tool_calls[].function.arguments` are now the non-stream twins of the two streamed carriers, resolved with the same client table (Issue #51).
- Machine-generated surfaces on the Oh My Pi protected route were cloaked by tool name in the declaration only. A system block or a tool description that named a declared shared or fallback tool (`wp_*`, `wp_ext_<hash>`) leaked the client's own source name to the model, because the prose pass never ran there. The route now builds its text-level cloak from the same effective declaration map the declaration pass used, which is what the alias-plan clients already did (Issue #51). The schema walk now applies the same request-scoped aliases to the prose one level deeper: a `description` or `title` inside a tool's `parameters` / `input_schema` reaches the model under the declared alias, in both carriers, while every structural and literal field (`required`, `enum`, `const`, `default`, `examples`, property names) stays byte-for-byte (Issue #51, Issue #50).
- **The Oh My Pi protected route now applies brand rewriting inside a tool's JSON Schema.** `rewriteProtectedBrand` rewrote only the tool's top-level `description`, so a nested `description` or `title` that named the client, its home directory or the vendor reached the model literal while the declaration kept the client's spelling. Those fields now go through the same schema walk (`rewriteToolProse`) the alias prose pass uses, under the walk's existing structural and literal exclusions; that alias pass still runs after this one and supplies the request-scoped tool aliases.
- `reverseFlushCloakedBrandLanes` compared each lane key against `prefix+match`. When draining every block at end of stream the prefix is empty while the key still carries its block prefix, so nothing matched and every still-held token was silently dropped instead of flushed.
- A streamed delta whose trailing token was held in a lane kept its original text, sending the held token inline and then a second time from the flush. The held remainder is now stripped from the emitted text, so the token arrives once.
- Pruned obsolete internal seams and test-only utilities from core production source: removed `resolveExplicitClient`, `(*explicitOMPLifecycleManager).reset`, legacy `splitSSEEvents` wrapper, and `corroborateCloakedTargetOMP` (Issue #42).
- Relocated test-only JSON exploration utilities (`walkJSON`, `appendPath`, `collectText`) to `json_test_helpers_test.go` (Issue #42).
- Consolidated duplicate round-trip streaming tests and client-specific uniqueness tests into canonical integration and table initialization suites, merging `session_cleanup_test.go` into `filter_test.go` (Issue #42).
- Dropped `TestOMP_CanonicalCollisionStillRejects`, a strict subset of `TestProtectedOMPEscapedCanonicalCollisionsReject["bare-plus-escaped"]` (same `read` + `_read` payload, same 503 assertion, and the surviving case also checks the exact error body and that no route was pinned). Each of the four per-issue suites stays: a coverage audit found none fully duplicated, with 11, 11, 16, and 17 of their tests having no counterpart elsewhere (PR #46).
- Two reversal tests asserted only that the payload did not contain the substring `omp`, which holds whether or not the reverse ran, so both passed while testing nothing. `TestReverseBrand_NonOMPGetsItsOwnWordNotOmp` now asserts the real contract - a non-Oh-My-Pi client reverses the bare brand word to its own (`Claude`) and never to `omp` - and the native-Antigravity case asserts that an unresolved client leaves the response byte-identical (PR #46).
- `reverseCloakedBrandStreamingMap` read the content index from the OpenAI event root, which carries none, so every streamed choice shared the `openai:0` brand lane and a token held back for choice 0 was emitted through choice 1 (`"A"` + `"hello "` arrived as `""` + `"Ahello "`). Each choice now uses its own `choices[].index` lane.
- A flushed held carry was re-encoded without its index - an Anthropic `content_block_delta` with no `index`, an OpenAI `choices[]` entry with none - so the client had no content block to attach the recovered text to. Each lane now flushes as its own indexed event through one shared encoder. The Anthropic form is a real SSE event frame (`event: content_block_delta` plus `data:`), because that is how the Anthropic Messages stream frames every event and a client that dispatches on the event name would otherwise never deliver the recovered text (Issue #47).
- `reverseFlushCloakedBrandLanes` resolved a carry with the owning lane's rule alone, so a token held open by a longer rule was emitted verbatim: `Antigravity` sat in the `Antigravity-api` lane (it is a prefix of that rule), and flushing it through that rule left the cloaked word in the client's stream. The carry is now run through the whole reverse table in its declared order, so the rule that owns the token inverts it.
- The tool-schema walk rewrote every string in the schema, including `required` members, so `required:["Claude"]` became `required:["Antigravity"]` while the property kept the name `Claude`: no arguments could satisfy the schema afterwards. Only the descriptive fields (`description`, `title`) are rewritten now; property names and the structural keywords (`required`, `enum`, `const`, `default`, `pattern`) are preserved.
- Windows paths inside streamed tool-call arguments were not restored. Those fragments arrive JSON-escaped, so the whole-path rule missed `C:\\Users\\me\\.gemini\\GEMINI.md` and only the bare `GEMINI.md` rule fired, handing the client a `.gemini` path that does not exist. The `claude_code` reverse table now also carries the escaped spelling of its path rules, and the pair is held across fragments like any other match.
- Brand reverse on the standalone (non-SSE) chunk path was gated on Oh My Pi, so a `codex` or `claude_code` session on an OpenAI-protocol exit received the cloaked word verbatim. Those exits hand the interceptor unframed JSON chunks (`sdk/api/handlers/openai/openai_handlers.go` wraps each one in `data: %s\n\n` only when writing to the client), which is exactly the shape the standalone branch handles; the SSE branch is the Anthropic `/v1/messages` case, where the host forwards the framed bytes verbatim. Every resolved client now applies and flushes its own table on both shapes: `reverseBrandStandalone` dispatches by client, `orderedBrandFlushes` resolves a carry through that client's table, a finished choice also marks the carrier lanes it opened so a cloaked-brand session is still freed at stream end, and the flush filter matches those lane keys by choice prefix.

## Candidate 0.6.0 (unreleased) - 2026-09-25

`0.6.0` is untagged: on release restore the bracketed heading, re-add its
compare link, and repoint `[Unreleased]` at `v0.6.0`.

### Verification (2026-09-27)

Not a release: no `v0.6.0` tag exists and none is proposed here. These results
describe a candidate linux/amd64 `-buildmode=c-shared` artifact built from
source `4e946ac` (SHA256
`a9b4e88833784e14063e25b0716bedbdebe91a615386394e0bd6ca4153ca013b`,
`vcs.modified=false`) and loaded as plugin version `0.6.0` on CLIProxyAPI
`v7.3.19`.

- Oh My Pi `18.3.4` live acceptance against that artifact: **7 of the 9 bare
  canonical tools** (`read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`)
  executed end-to-end under their bare spelling
  (declaration -> upstream alias -> exact restore -> native execution ->
  continuation). The remaining two, `ask` and `web_search`, plus shared
  aliases, deterministic fallback aliases and `xd://` device dispatch, are
  recorded as **escaped-wire** coverage: those cases ran as `_ask` /
  `_web_search` over `anthropic-messages` and are not counted as bare
  acceptance. Per-case evidence:
  [OMP checklist](docs/verification-checklist-omp.md#verified-results---2026-09-27-plugin-v060).
- Live rejection checks: a conflicting marker and a declaration collision on
  `agy/` both return HTTP 503 `omp_cloak_required` with no upstream attempt,
  and an explicit OMP marker on a non-`agy/` route mutates nothing.
- Claude Code `2.1.283` and OpenAI Codex `codex-cli 0.157.1` (via
  `opencodex 2.67.0`) also ran against the same artifact. Codex passed in both
  `exec` (code mode) and `exec_command` (shell mode) with exact restoration,
  real execution and continuation, including direct top-level
  `mcp__fastctx__*` declarations restored from `wp_ext_<hash>` fallbacks.
  Claude Code mapping was verified at declaration level (20/20, no source
  leakage) while its upstream completion returned HTTP 429. Both are recorded
  as **manual verification pending/deferred by the user**; until those manual
  checks land, Issue #40 is not complete and nothing in this section is
  release-ready.

### Added
- Generalized request-scoped alias plan architecture for dynamic-surface clients (Claude Code, OpenAI Codex) alongside extended alias handling for Oh My Pi, with fail-closed correlation (`tool_cloak_required` for alias-plan clients, `omp_cloak_required` for Protected OMP), collision detection, and deterministic reversible fallback aliases (`wp_ext_<hash>`) for dynamic/MCP tool declarations (Issue #32).
- Full declaration-level cloaking for Claude Code (`claude_code`): core tools map to AGY role names (`Glob -> find_by_name`, `WebFetch -> read_url_content`), Tier-2 tools map to stable shared aliases (`wp_*`), and deferred tools/placeholders are handled across normal protocol identity positions (`tools[]`, `tool_calls`/`tool_use`, `tool_choice`) (Issue #36, Issue #32).
- Extended OMP tool coverage beyond the canonical nine on Protected routes via shared aliases (`wp_*`) and deterministic fallback aliases (`wp_ext_<hash>`), while preserving `xd://` virtual device transport and protocol integrity (default-profile smoke verified 2026-09-27; full three-client acceptance tracked under Unreleased) (Issue #37, Issue #32).
- Protected OMP config-time validation rejecting mutable canonical nine, non-injective targets, and invalid target naming in `tool_mappings` during initialization and reconfigure (Issue #37, Issue #32).
- Full declaration-level cloaking for OpenAI Codex (`codex`) across code mode and shell mode (`exec_command -> run_command`, `apply_patch -> wp_apply_patch`, `write_stdin -> wp_write_stdin`, `view_image -> wp_view_image`) with request-scoped reversal preventing cross-mode collision (Issue #38, Issue #32).
- Namespace collision gate failing closed when distinct source identities or namespace variants resolve to the same final target or base identity (Issue #32).
- Missing RequestID or duplicate RequestID rejection with HTTP 503 `tool_cloak_required` for alias-plan clients (Issue #32).

### Changed
- Aligned Codex collaboration tools (`collaboration__send_message -> wp_send_message`, `collaboration__list_agents -> wp_list_workers`, `collaboration__followup_task -> wp_collaboration_followup_task`) to cross-client shared vocabulary to avoid premature speculative AGY mapping (Issue #38, Issue #32).
- Added full-coverage tie-breaker in `detectCloakedClientWithSignal` so clients with 100% table match are not misattributed to partial superset tables under identical ratio and hit counts (Issue #36).

## [0.5.2] - 2026-09-12

### Added
- Cloaked Codex traffic on the code-mode wire surface: `exec -> run_command`, `web_search -> search_web`, `request_user_input -> ask_question`, and the flattened `collaboration__spawn_agent` / `collaboration__followup_task` / `collaboration__list_agents` children to `invoke_subagent` / `manage_task` / `manage_subagents`. Only names that occupy a tool-name position are listed; helpers that exist solely as prose inside the `exec` description stay pass-through (PR #31).
- Shell-mode declaration inventory (`exec_command`, `write_stdin`, `apply_patch`, `view_image`, ...) retained for detection only, so a shell-mode session still resolves as Codex without the table carrying two sources that want one target (PR #31).
- Codex surface reference and ADR 0004 recording which names the wire carries in each mode, why MCP stays pass-through, and how the reverse table is scoped (PR #31).

### Fixed
- Codex sessions were detected but never cloaked: a request declaring `exec` plus namespaced children scored one hit against a floor of two, because namespaced declarations were not reduced to their base identities and keys configured through `tool_mappings` never counted (PR #31).
- The reverse table was narrowed against the executed body, which carries no source name, so an already-cloaked Codex stream lost its reverse and payload tool names passed through unmutated (PR #31).
- A natively declared AGY target is no longer handed back as a Codex source name the client never declared, including on the correlated response path, which now reuses the scope the request interceptor derived from the raw body (PR #31).

## [0.5.0] - 2026-09-07
### Added
- Adopted the 9-tool AGY CLI-native Safe Mapping Set for Oh My Pi (`read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, `web_search -> search_web`) with static source and target identity inventories (Issue #26).
- Fail-closed Protected OMP routing for explicit OMP markers on `agy/*` routes with strict single-document JSON validation, base identity declaration collision rejection, and exact 503 JSON rejection (`omp_cloak_required`) before upstream execution (Issue #27).
- Request-scoped active reverse authority: only canonical pairs whose source declaration was actually transformed become active for reverse uncloaking in correlated responses and streams (Issue #27).
- Durable `ExplicitOMPNonAGYBypass` path for marked OMP traffic on non-`agy/` routes, pinning zero-mutation bypass state by host `RequestID` across request, response, and stream (Issue #27).
- Request lifecycle management via CLIProxyAPI `request_lifecycle_plugin` and `request.complete` callback, keeping route state logically separate from disposable SSE state with pre-payload rehydration support (Issue #27).
- Terminal canonical Protected brand policy: `Oh My Pi`, `oh-my-pi`, and `omp` mask to terminal `Antigravity` without operator override or reprocessing, while preserving literal `.omp` path segments and restoring assistant-visible `Antigravity -> omp` (Issue #27).
- Deterministic release-gate acceptance test suite proving all nine canonical Safe Mapping pairs, transport exceptions, mixed pass-through traffic, RequestID correlation, and lifecycle invariants (Issue #28).
- Configured isolated `cloak-live` profile with explicit `X-Cloak-Client: oh_my_pi` marker (Issue #28).

### Changed
- Converted `todo`, `hub`, `eval`, `vibe_*`, and Autoresearch tools into intentional pass-through tools that remain unmutated (Issue #26).
- Replaced `glob -> list_dir` with `glob -> find_by_name` (Issue #26).
- Clarified that canonical AGY targets alone are corroboration-only and cannot authoritatively attribute no-marker traffic to OMP (Issue #26).
- Updated documentation (`README.md`, `CONTEXT.md`, `AGENTS.md`, `docs/verification-checklist.md`, `docs/specs/oh-my-pi-cloaking-spec.md`) and live test fixtures (`tests/test_omp.py`, `tests/test_client.py`) to the shipped Safe Mapping Set and routing contracts (Issue #28).

## [0.4.4] - 2026-09-06

### Fixed
- Gate request-body brand rewriting and tool cloaking on a resolved supported client identity, preserving ordinary API request bodies when no supported client is identified.
- Preserve client-precedence semantics across explicit `X-Cloak-Client`, verified User-Agent evidence, and body-based classification while keeping recognized Oh My Pi round trips intact.

### Changed
- Returned release numbering to SemVer so CLIProxyAPI Plugin Store discovery and asset matching use the expected `v<version>` tag convention.

## [26.09.06] - 2026-09-06

### Changed
- Switched release numbering from SemVer to date-based CalVer (`YY.MM.DD`), with Git tags using the `v.YY.MM.DD` form.

## [0.4.3] - 2026-09-02

### Fixed
- Preserved only the exact dot-prefixed `.omp` filesystem path segment during forward OMP brand rewriting, while continuing to mask bare `omp`, `/omp/`, `\omp\`, `.omp-backup`, `profile.omp`, and other brand aliases.

### Removed
- Removed obsolete Claude-specific repository guidance after retiring that workflow.

## [0.4.2] - 2026-09-01

### Added
- Added deterministic `X-Cloak-Client` request identity override with control-header consumption so the header never leaks upstream.
- Added conservative positive User-Agent evidence for thin Oh My Pi requests while preserving body-based classification as fallback.
- Added Oh My Pi response identity restoration: assistant-visible standalone `Antigravity` text is rewritten back to `omp` without touching tool arguments or unrelated data.

### Fixed
- Preserved invalid explicit-client precedence across response and stream paths so weaker UA evidence cannot override a bad explicit signal.
- Hardened reverse-brand streaming across OpenAI and Anthropic terminal events, fragmented semantic lanes, singleton content maps, and multi-choice completion.
- Ensured Anthropic standalone terminal flushes keep correct data framing.

## [0.4.1] - 2026-08-29

### Added
- Added ADR 0002 for ratio-ranked client classification and request/session pre-registration.
- Added an offline integration/lifecycle test suite and Python CI validation.

### Changed
- Streamlined repository documentation and synchronized GitNexus guidance/statistics.

## [0.4.0] - 2026-08-27

### Added
- Added Oh My Pi Vibe Mode tool cloaking (`vibe_*`).
- Added Oh My Pi Autoresearch tool cloaking (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`).

## [0.3.1] - 2026-08-27

### Fixed
- Fixed streamed Oh My Pi tool-call uncloaking so Antigravity tool names are restored correctly at the OMP client boundary.

### Added
- Added integration scripts covering Oh My Pi and streaming client behavior.

## [0.3.0] - 2026-08-27

### Added
- Added Oh My Pi client detection and bidirectional tool-name cloaking.
- Added remote VPS custom-plugin installation documentation.

### Changed
- Synchronized upstream coding-agent brand keywords.

## [0.2.0] - 2026-06-29

### Added
- Added `model_prefixes` gating so cloaking can be restricted to selected model/provider prefixes.

### Fixed
- Fixed client detection behavior to make cloak/uncloak classification more reliable.

## [0.1.1] - 2026-06-28

### Fixed
- Renamed the Go module path to the fork repository path so module metadata matches distribution.

## [0.1.0] - 2026-06-28

### Added
- Added structured tool-name cloaking on requests and symmetric uncloaking on responses/streams.
- Added reload hardening for runtime configuration changes.
- Added plugin store registry metadata for distribution.

## [0.0.3] - 2026-06-20

### Added
- Added request-side rewriting of coding-agent identity signals to `Antigravity`.

## [0.0.2] - 2026-06-19

### Added
- Added configurable brand-rewrite keywords.

## [0.0.1] - 2026-06-19

### Added
- Initial plugin implementation.
- Added repository metadata, MIT license, and release build workflow.

[Unreleased]: https://github.com/monet88/antigravity-cloak/compare/v0.5.2...HEAD
[0.5.2]: https://github.com/monet88/antigravity-cloak/compare/v0.5.1...v0.5.2
[0.5.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.5.0
[0.4.4]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.4
[26.09.06]: https://github.com/monet88/antigravity-cloak/releases/tag/v.26.09.06
[0.4.3]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.3
[0.4.2]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.2
[0.4.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.1
[0.4.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.0
[0.3.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.1
[0.3.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.0
[0.2.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.2.0
[0.1.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.1
[0.1.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.0
[0.0.3]: https://github.com/monet88/antigravity-cloak/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/monet88/antigravity-cloak/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/monet88/antigravity-cloak/tree/v0.0.1
