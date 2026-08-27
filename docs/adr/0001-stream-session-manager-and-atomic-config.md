# ADR 0001: Stream Session Manager and Atomic Configuration

## Status
Accepted

## Context
CLIProxyAPI v7.2.143 introduced schema_version >= 3 where `OriginalRequest` and `RequestBody` are only populated on the header-init chunk (`ChunkIndex == StreamChunkHeaderInitIndex`) and omitted on subsequent payload chunks. Previously, re-detection on every chunk failed under schema >= 3. Additionally, concurrent requests and hot streaming paths suffered from RWMutex contention when reading configuration snapshots.

## Decision
1. **Encapsulate Stream Buffering & Session Caching**: Implement `streamSessionManager` to:
   - Cache uncloak regex patterns on header-init using `RequestID` (or fallback metadata/header identifiers/anonymous single-stream fallback).
   - Reassemble incomplete TCP fragments across SSE event boundaries (`\n\n`).
   - Prune abandoned or stale sessions (> 5 minutes) opportunistically on chunk arrival and session initialization.
   - Clean up sessions immediately on `data: [DONE]`.

2. **Immutable Atomic Configuration Publication**: Replace `sync.RWMutex` with `atomic.Pointer[filterConfig]`:
   - Config updates build a full, immutable `filterConfig` struct (including precompiled uncloak regular expressions).
   - Hot path reads (`activeFilterConfig()`) perform an atomic load without locking or deep copying.

## Consequences
- Full compatibility with CLIProxyAPI schema_version >= 3 without breaking legacy schema < 3 mode.
- SSE stream uncloaking functions reliably across fragmented TCP chunks.
- Lock-free configuration reads on all request, response, and stream interceptor hooks.
