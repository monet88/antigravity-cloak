# System Conventions Sanitization Spec

## Problem and decision

Local bisection of an Oh My Pi (`omp/18.1.18`) request to
`agy/gemini-3.8-flash` isolated the exact `<system-conventions>` wrapper in the
system prompt as the trigger for `429 RESOURCE_EXHAUSTED`. Renaming the wrapper
to `<agent-conventions>` succeeded in the reported client-side test. The chosen
plugin replacement is `<conventions>`; installed-binary live acceptance is
a separate verification step.

The enclosed RFC 2119 instructions remain unchanged. The fix belongs in the
proxy so OMP installations do not require bundle patching after each update.
The internal upstream classifier and its handling of other tags are not part
of this contract. See [ADR 0003](../adr/0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md).

## Scope and ordering

- Hook: `request.intercept_before`, in `handleProtectedAGY` after tool cloaking
  and brand rewriting, before post-transform validation and serialization.
- Route: explicit OMP marker plus an `agy/` prefix on `Model` or `RequestedModel`.
  ProtectedAGY retains precedence over generic `model_prefixes`.
- Fields: top-level `system`, plus `messages[].content` for roles `system` and
  `developer`. Supported content is a string or an array of `text`/`input_text`
  blocks; only each block's string `text` field is visited.
- User, assistant, and tool messages, tool definitions/arguments, block metadata,
  image data, and unsupported content shapes are preserved by this step.
- Explicit OMP non-AGY bypass and generic client routes do not run this step.
- Brand configuration cannot disable sanitization. The transformation does not
  run on responses or streams and has no reverse mapping.

## Exact replacement

```go
func sanitizeSystemConventions(text string) (string, bool) {
    next := strings.ReplaceAll(text, "<system-conventions>", "<conventions>")
    next = strings.ReplaceAll(next, "</system-conventions>", "</conventions>")
    return next, next != text
}
```

The replacement is case-sensitive, affects every exact occurrence in the selected
text, and is idempotent. Bare `system-conventions`, filenames, similar tag names,
and `<system-directive>` remain unchanged. Exact tags quoted inside selected
system/developer text are also replaced. Attribute-bearing and differently cased
tags are outside this fixed-wrapper contract. Processing cost depends on input
length; replacements allocate output strings.

## Verification

`system_conventions_test.go` covers exact/repeated tags, Unicode, unchanged
strings, idempotence, text blocks and excluded metadata, and the real plugin
request handler for OpenAI and Anthropic formats. Handler cases exercise
ProtectedAGY, RequestedModel routing, disabled default brand keywords, a
mismatched generic model gate, non-AGY bypass, unmarked traffic, and other clients.

Run `go test ./...`, `go test ./.github/scripts`, and `go vet ./...`.
These offline checks do not establish live upstream acceptance.

## Build and local deployment

Build the merged source as a Linux/amd64 CGO shared library. Use
[the local verification runbook](../verification-checklist.md) to discover the
actual Compose service, config, plugin mount, and log mount; do not hardcode
a historical gateway path.

Stop the gateway before replacing a loaded `.so`, retain a rollback copy outside
the plugin discovery directory, and recreate the service without pulling a new
gateway image. Verify plugin version, artifact checksum, and registration.

Use the existing `cloak-live` OMP profile for a controlled streamed tool-call
test. Prove that the original system wrapper reaches the plugin, the rewritten
upstream prompt uses `<conventions>`, and a streamed tool call is restored to an
OMP-native name and executed. Disable debug logging, recreate the service, and
truncate the debug log after the test. Record a live failure separately from
the deterministic transformation result.
