# ADR 0002: Ratio-Ranked Client Classification and Stream Session Pre-Registration

## Status
Accepted

## Context
When integrating Oh My Pi (`oh_my_pi`) and handling streaming responses across various upstream model providers (such as Gemini 2.5/3.7 via CLIProxyAPI), two critical failure modes were diagnosed (see [NOTE-DEBUGS.md](../../NOTE-DEBUGS.md#bug-001-oh-my-pi-stream-chunk-uncloak-failure-assistant-returned-empty-stop-after-retry-cap)):

1. **Static Table-Length Threshold Failure**:
   The previous `detectCloakedClient` algorithm calculated the match ratio against the total tool count in the client's static mapping table:
   $$\text{ratio} = \frac{\text{hits}}{\text{len}(\text{cloakTable})}$$
   When Oh My Pi sent standard coding sessions with its 9 default tools (`read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`, `ask`, `todo`), its match ratio against the 12-tool table was $9/12 = 75\%$, which fell below the static $80\%$ threshold. As a result, detection returned empty (`""`), and upstream Antigravity tool names (e.g., `run_command`) failed to uncloak back to native Oh My Pi tools (`bash`), causing the client to terminate with retry errors.

2. **Stream Chunk Inference Fragility**:
   Under CLIProxyAPI `schema_version >= 3`, request bodies and tool declarations are omitted from intermediate streaming chunks. Attempting to infer client identity on every chunk from incomplete delta payloads is computationally expensive and brittle, especially across fragmented TCP packets or non-SSE JSON chunk streams.

3. **Native Antigravity Traffic False Positives**:
   When a native Antigravity client issues a request with common Antigravity tools (`run_command`, `view_file`, `replace_file_content`), multiple uncloak tables (Claude Code, Oh My Pi, Codex) could match $100\%$ of the subset. Without disambiguation, native traffic could be inadvertently mutated.

## Decision

1. **Ratio-Ranked Classification against Observed Tools**:
   - Calculate detection ratios against the **observed unique tool count** rather than the static table length:
     $$\text{hits} \times 5 \ge \text{totalObserved} \times 4 \quad (\ge 80\%)$$
   - Require a minimum threshold of $\text{hits} \ge 3$ to avoid spurious single-tool triggers.
   - For native Antigravity superset requests where multiple distinct cloak tables reach $100\%$ match ($\text{fullCoverageCount} \ge 2$), explicitly return `""` (no-op passthrough) to prevent corrupting native Antigravity traffic.
   - Use deterministic integer ratio comparison ($h_1 \times t_2 > h_2 \times t_1$) to rank candidate clients without floating-point inaccuracies.

2. **Request-Time Stream Session Pre-Registration**:
   - In `request.intercept_before`, as soon as client detection succeeds on the full request body, pre-register the detected client and its uncloak regex patterns in `StreamSessionManager` keyed by `RequestID` (and metadata correlation identifiers).
   - On subsequent stream chunk intercept hooks (`response.intercept_stream_chunk`), resolve the active session by `RequestID` in $O(1)$ time without repeating body parsing or client detection.

3. **Universal Chunk Protocol Support in Stream Processing**:
   - In `processChunk`, detect both standard SSE event frames (`data:`, `event:`, `\n\n`) and standalone newline-free OpenAI JSON chunk objects, ensuring reliable uncloaking across both streaming formats.

## Consequences

- Oh My Pi default sessions (9 tools), Vibe Mode extensions (`vibe_*`), and Autoresearch extensions (`*_experiment`) are reliably detected and uncloaked.
- Native Antigravity requests pass through cleanly without false positive rewrites.
- Zero per-chunk client detection overhead on the streaming hot path.
- Trade-off: Stream requests without correlation keys (`RequestID` or matching headers) must fall back to header-init parsing or pass through without uncloaking to maintain session isolation.

## References
- [NOTE-DEBUGS.md](../../NOTE-DEBUGS.md): Bug 001 root cause and verification.
- [docs/specs/oh-my-pi-cloaking-spec.md](../specs/oh-my-pi-cloaking-spec.md): Complete specification for Oh My Pi cloaking and keyword synchronization.
- [ADR 0001](0001-stream-session-manager-and-atomic-config.md): Stream session manager and atomic configuration lifecycle.
