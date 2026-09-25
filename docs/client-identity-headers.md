# Client identity headers

Use `X-Cloak-Client` on the request that actually reaches CLIProxyAPI (CPA) to
identify the client independently of its current tool set. A thin request or a
request containing only dynamic tools may not satisfy body-detection thresholds.

| Client | Header value |
| --- | --- |
| Claude Code | `claude_code` |
| Codex | `codex` |
| Oh My Pi | `oh_my_pi` (also accepts `omp` and `oh-my-pi`) |

These are client identities, not model names or mode selectors. Use the exact
Claude Code value `claude_code`; `claude` and `claude-code` are not built-in aliases.
The plugin consumes and clears this control header before forwarding upstream.
It is an attribution hint, not an authentication credential.

With the default client tables, a valid explicit marker wins over UA/body
detection. Claude Code and Codex still pass through the configured model gate;
their markers do not grant OMP's ProtectedAGY routing policy. Explicit OMP on
non-`agy/` routes still selects the durable no-mutation bypass.

Configuration syntax was checked against official documentation on 2026-09-25.
This guide documents supported configuration, not a claim that a particular
running client or gateway has been configured or live-tested. The broader alias
policy in [issue #32](https://github.com/monet88/antigravity-cloak/issues/32) remains
separate implementation work.

## Claude Code: direct connection to CPA

Claude Code supports `ANTHROPIC_CUSTOM_HEADERS` as `Name: Value` pairs, separated
by newlines. Keep the existing CPA base URL, credentials, and model configuration.

For one PowerShell session, when no other custom headers are configured:

```powershell
$env:ANTHROPIC_CUSTOM_HEADERS = "X-Cloak-Client: claude_code"
claude
```

For persistent CLI configuration, merge this entry into the existing `env` block
of `%USERPROFILE%\.claude\settings.json` (`~/.claude/settings.json`):

```json
{
  "env": {
    "ANTHROPIC_CUSTOM_HEADERS": "X-Cloak-Client: claude_code"
  }
}
```

Preserve existing settings and custom headers. For multiple headers, use `\n`
inside the JSON string:

```json
{
  "env": {
    "ANTHROPIC_CUSTOM_HEADERS": "X-Existing: value\nX-Cloak-Client: claude_code"
  }
}
```

Use one marker value, replacing any previous `X-Cloak-Client` entry rather than
adding a conflicting identity. Restart Claude Code after changing startup
configuration. A settings-file `env` value takes precedence over a shell value
for the same variable. Shell configuration reaches only processes started from
that shell; use the appropriate launcher configuration for other surfaces.

Source: [Claude Code gateway configuration and additional headers](https://code.claude.com/docs/en/llm-gateway-connect#send-additional-headers).

## Codex: native provider configuration

Codex supports static `http_headers` and environment-backed `env_http_headers`
inside a model-provider configuration. These belong to the model provider, not
to an `mcp_servers` entry.

In the active user configuration (`$CODEX_HOME/config.toml`, normally
`~/.codex/config.toml`, or `%USERPROFILE%\.codex\config.toml` on Windows), add the
header to the existing provider that sends requests toward CPA:

```toml
[model_providers.cpa]
http_headers = { "X-Cloak-Client" = "codex" }
```

This is a fragment to merge, not a complete provider definition. Replace `cpa`
with the active provider ID and retain its existing `name`, `base_url`, auth,
wire format, and other headers. Do not duplicate an existing TOML table. Ensure
the selected `model_provider` or active profile selects that provider. Use the
configuration owned by the actual launcher if it sets a different `CODEX_HOME`;
do not assume an edit to the default home reaches every Codex process.

Alternatively, merge an environment-backed header into that same provider:

```toml
[model_providers.cpa]
env_http_headers = { "X-Cloak-Client" = "CLOAK_CLIENT" }
```

Then start Codex from a PowerShell session that sets the value:

```powershell
$env:CLOAK_CLIENT = "codex"
codex
```

Choose either the static or environment-backed form for this header. A desktop
launcher does not inherit an unrelated terminal's environment. Start a fresh
Codex session after configuration changes. Current official documentation places
provider settings in user-level configuration; do not rely on a repository's
`.codex/config.toml` to override provider routing or headers.

Sources: [OpenAI custom model providers](https://developers.openai.com/codex/config-advanced#custom-model-providers)
and [configuration reference](https://developers.openai.com/codex/config-reference).

### Direct identity does not prove wire compatibility

The native header mechanism proves how to attach a marker to provider requests.
It does not prove that every direct Codex request format is supported by the
current cloak plugin. The repository's dated accepted Codex path uses opencodex
to lower Responses into Chat Completions before CPA. A marker alone cannot add
missing traversal for a different declaration/history format.

Verify the actual format at CPA, tool-name transformation, and client execution
before calling a new direct route compatible. See the
[Codex surface reference](research/codex-tool-surface-2026-09-12.md) and
[ADR 0004](adr/0004-cloak-codex-tool-names-by-wire-position.md).

## Codex through opencodex

For `Codex -> opencodex -> CPA`, configure the **CPA-bound opencodex provider**,
not just Codex's first-hop provider. Merge this into the existing provider entry
in opencodex's active configuration (normally `~/.opencodex/config.json`):

```json
{
  "providers": {
    "cpa": {
      "headers": {
        "X-Cloak-Client": "codex"
      }
    }
  }
}
```

Preserve the provider's other fields and any other headers; use its actual ID if
it is not `cpa`. For the runtime documented by this repository, run `ocx restart`
after saving the provider change, then start a fresh request. Restarting interrupts
that proxy's active work, so perform it between requests.

The inspected `openai-chat` adapter constructs outgoing headers from content
type, authentication, and `provider.headers`. Do not assume an arbitrary incoming
header automatically survives the proxy hop. A provider shared by Claude Code and
Codex must not unconditionally attach `codex` to both: use separate correctly
marked provider routes or verified per-request identity forwarding.

Evidence: [dated local acceptance](research/codex-tool-surface-2026-09-12.md),
plus the local opencodex source snapshot `c155cc7923dbc0102e27d79185505a85d4357b2c`,
`src/adapters/openai-chat.ts`, `openAIChatTransport` (provider header merge).
This source check is not a new live validation of the installed proxy.

## Verify the complete path

1. Confirm the selected provider reaches the intended CPA instance. If there is
   an intermediate proxy, verify the marker on its CPA-bound request.
2. Confirm the plugin receives the expected valid marker and resolves the client.
   A configured value or a successful model response is not proof of arrival.
3. Confirm the control header is absent from the request forwarded beyond CPA,
   including model-gate skips. Confirm the desired model is eligible for cloaking.
4. Check transformed declarations, restored response/stream names, and a real
   client tool execution with continuation. Header-based identity does not prove
   that all tools cloak, or that issue #32 has been implemented.

For controlled capture/debugging, follow the ordered
[verification runbook](verification-checklist.md); do not enable full-body debug
logging for ordinary use. Retain only redacted evidence, never credentials or
full request-body logs in the repository.
