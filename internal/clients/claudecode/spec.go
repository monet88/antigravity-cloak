package claudecode

import (
	"slices"

	"github.com/monet88/antigravity-cloak/internal/spec"
)

// ClientID is the canonical wire identifier for Claude Code.
const ClientID = "claude_code"

// NewsURL is the exact source link from the captured system sentence (Issue #55 Contract A).
const NewsURL = "https://www.anthropic.com/news/claude-fable-5-mythos-5"

// NewsURLSanitized is the neutral root the recognized sentence carries upstream.
const NewsURLSanitized = "https://antigravity.google"

// NewsSentencePrefix forms the two recognized sentences together with the exact URL.
const NewsSentencePrefix = "If the person asks about the differences between the two, "

// AgentToolSourceName is the native Claude Code subagent tool (Issue #55 Contract B).
const AgentToolSourceName = "Agent"

// AgentModelDescriptionPrefix is the recognizable help signature of the captured Agent declaration.
const AgentModelDescriptionPrefix = "Optional model override for this agent."

// Spec returns the declarative data-only specification for Claude Code.
func Spec() spec.ClientSpec {
	// Claude Code keeps its file name too, because CLAUDE.md is brand-bearing
	// prose the model would otherwise quote straight back.
	contextMappings := spec.PathRules(".claude", ".gemini", "CLAUDE.md", "GEMINI.md")
	reverseContextMappings := spec.PathRules(".gemini", ".claude", "GEMINI.md", "CLAUDE.md")

	forwardBrandMappings := slices.Concat(
		[]spec.RewriteMapping{
			// Opening identity lines. Each names the vendor twice, so token substitution
			// alone yields nonsense ("Antigravity, Google's official CLI for
			// Antigravity"). The replacements are the verbatim <identity> lines used by
			// the real Antigravity CLI, for both its main session and its subagents, so a
			// cloaked request is indistinguishable from native traffic.
			// Must precede every other "Claude"/"Anthropic" rule.
			{Match: "You are Claude Code, Anthropic's official CLI for Claude.", Replacement: spec.AntigravityIdentity},
			{Match: "You are a Claude agent, built on Anthropic's Claude Agent SDK.", Replacement: spec.AntigravityIdentity},
			{Match: "Claude Code", Replacement: "Antigravity"},
			// The context group Claude Code injects into the system context, global
			// and project-local alike, per its documented memory hierarchy:
			//
			//   ~/.claude/CLAUDE.md        user's private global instructions
			//   ~/.claude/rules/*.md      user rules
			//   ~/.claude/projects/<slug>/memory/   per-project auto memory
			//   ./.claude/CLAUDE.md        project instructions
			//   ./.claude/rules/*.md       project rules
			//
			// Built by contextMappings, which matches the home directory as a
			// whole segment at any position because that is the only spelling Claude
			// Code actually emits on Windows. See spec.PathRules.
		},
		contextMappings,
		[]spec.RewriteMapping{
			// Only claude_code needs this: its table also carries the bare "Claude"
			// rule, which would otherwise consume the vendor prefix inside CLAUDE.md
			// and produce Antigravity.md - which the reverse can only restore to
			// Claude.md, corrupting the file name's case. Codex and Oh My Pi have no
			// bare-Claude rule, so their CLAUDE.md passes through untouched already.
			{Match: "CLAUDE.md", Replacement: "AGENTS.md"},
			// Official product/URL names, taken from antigravity.google and its docs.
			// The product is "Antigravity SDK" (pip install google-antigravity); the
			// platform lives on antigravity.google.
			//
			// These two forward rules share one target deliberately: the single
			// reverse rule ("Antigravity SDK" -> "Anthropic SDK") is the approved
			// resolution, so a client that wrote "Claude Agent SDK" reads "Anthropic
			// SDK" back. Do not add a second reverse target to tell them apart; no
			// evidence-backed one exists.
			{Match: "Anthropic SDK", Replacement: "Antigravity SDK"},
			{Match: "Claude Agent SDK", Replacement: "Antigravity SDK"},
			{Match: "claude.ai", Replacement: "antigravity.google"},
			// Deliberately no client model IDs here. All four rows that used to sit
			// here - claude-fable-5-1 and claude-opus-5-5 (both mapped onto
			// gemini-3.1-pro-low, two sources with one target), claude-sonnet-5
			// (gemini-3.8-flash) and claude-haiku-4-5-20251001
			// (gemini-3.5-flash-lite) - were one-way: none of them can be inverted
			// from a route the gateway serves, and keeping only the two that look
			// one-to-one would hand the client back an AGY model name it never
			// wrote. A one-way rewrite is the failure this table exists to avoid:
			// the request looks clean while the client is handed a model name it
			// cannot resolve. Brand-masking a prose model ID is not worth that.
			// Claude Code's multi-agent workflow tool. Antigravity ships the same
			// capability as teamwork_preview_layer, so a cloaked request names the tool
			// Antigravity traffic would actually use. It is a client-specific surface,
			// not a brand token.
			{Match: "Workflow", Replacement: "teamwork_preview_layer"},
			// Plural of the tool name above. Matching is word-bounded, so "Workflows" in
			// prose is a distinct token and would otherwise survive. Grammar is
			// deliberately not repaired, same policy as the bare-brand rules below.
			{Match: "Workflows", Replacement: "teamwork_preview_layer"},
			// Bare vendor name. Mapped to "Google Deepmind" rather than a plain "Google"
			// so the reverse pass has a token specific enough to match safely: a bare
			// "Google" would fire on ordinary response prose, while "Google Deepmind" is
			// exactly the vendor's own name and nothing else.
			{Match: "Anthropic", Replacement: "Google Deepmind"},
			// Catch-all. Must stay after every longer "Claude" form above.
			{Match: "Claude", Replacement: "Antigravity"},
		},
	)

	reverseBrandMappings := slices.Concat(
		// The context group, including the JSON-escaped spelling of its Windows
		// file rule that arrives inside streamed tool-call arguments. Generated by
		// spec.PathRules with the pairs swapped, so the reverse of every forward rule is
		// its exact mirror and the escaped twin cannot drift out of the group.
		reverseContextMappings,
		[]spec.RewriteMapping{
			// Deliberately no bare "GEMINI.md" rule: the forward pass always
			// rewrites the directory with it, so matching the file name alone would
			// invert a path the forward pass never produced. That drift is what
			// collapsed a README edit into a no-op during live acceptance.
			// Inverse of the forward domain rule. It has to precede the bare brand
			// word below, or "Antigravity" would match inside "antigravity.google"
			// and the client would receive "Claude.google".
			{Match: "antigravity.google", Replacement: "claude.ai", ArgumentSafe: true},
			// Inverse of the bare vendor rule. Safe to reverse precisely because the
			// replacement is the two-word vendor name and not the bare "Google", which
			// would collide with ordinary prose in model output.
			{Match: "Google Deepmind", Replacement: "Anthropic"},
			// A client-declared identifier, like a tool name: the skill slug lives in
			// the client's own registry, so the response has to hand back a slug the
			// client can actually resolve. Without this the model calls
			// Skill("Antigravity-api") and the client answers "Unknown skill".
			{Match: "Antigravity-api", Replacement: "claude-api", ArgumentSafe: true},
			{Match: "Antigravity SDK", Replacement: "Anthropic SDK", ArgumentSafe: true},
			// Inverse of the prose rule that names this client's multi-agent
			// workflow tool. Antigravity ships the capability as
			// teamwork_preview_layer, so the model reads - and may echo - that name
			// in its prose; the client only ever declared "Workflow". A mention in
			// prose is not a declaration, so the exact alias-plan authority cannot
			// restore it and this table has to. The plural forward rule maps back
			// onto the same singular name: grammar is not repaired in either
			// direction.
			//
			// Prose only - deliberately not ArgumentSafe. The forward pass writes
			// this literal into prompts, never into a tool argument, so a
			// teamwork_preview_layer literal inside an argument is the client's own
			// text (a command the user typed, a file the model was told to write)
			// and rewriting it corrupts data the client executes. The tool NAME in
			// an argument or a declaration is a different surface: it round-trips
			// through the alias plan's own exact authority.
			{Match: "teamwork_preview_layer", Replacement: "Workflow"},
			// The bare brand word, listed last so the two longer "Antigravity" tokens
			// above win the prefix. This client's forward pass produced that word only
			// from its own name, so inverting it here is unambiguous.
			{Match: "Antigravity", Replacement: "Claude"},
		},
	)

	return spec.ClientSpec{
		ID:                     ClientID,
		UsesAliasPlan:          true,
		ContextMappings:        contextMappings,
		ReverseContextMappings: reverseContextMappings,
		ForwardBrandMappings:   forwardBrandMappings,
		ReverseBrandMappings:   reverseBrandMappings,
		DefaultCloakTools: map[string]string{
			// Tier-1: Core file/shell tools — proven one-to-one AGY role names.
			"Bash": "run_command", "Edit": "edit_file", "Read": "read_file",
			"Write": "write_file", "Grep": "grep_search",
			"Glob": "find_by_name", // Issue #36: was list_dir — AGY list_dir lists one dir, Glob is a pattern matcher.
			// Tier-1: Subagent/interaction — proven AGY role names.
			"Agent": "invoke_subagent", "AskUserQuestion": "ask_question",
			// Tier-1: Web tools — proven AGY role names (one-to-one, no schema conflict).
			"WebSearch": "search_web",
		},
		SharedAliases: map[string]string{
			// Discovery / workflow.
			"ToolSearch": "wp_find_tools",
			"Skill":      "wp_invoke_skill",
			"Workflow":   "wp_run_workflow",
			// Web fetch (read_url_content was removed in AGY 2.25.0+).
			"WebFetch": "wp_fetch_url",
			// Subagent control — unproven semantic match to AGY targets.
			"ListAgents":  "wp_list_workers",
			"SendMessage": "wp_send_message",
			"TaskStop":    "wp_cancel_task",
			// Scheduling / planning / worktree.
			"ScheduleWakeup": "wp_set_wakeup",
			"CronCreate":     "wp_create_schedule",
			"CronDelete":     "wp_delete_schedule",
			"CronList":       "wp_list_schedules",
			"EnterPlanMode":  "wp_begin_planning",
			"ExitPlanMode":   "wp_finish_planning",
			"EnterWorktree":  "wp_open_worktree",
			"ExitWorktree":   "wp_close_worktree",
			// Notebook / reporting.
			"NotebookEdit":   "wp_edit_notebook",
			"ReportFindings": "wp_submit_report",
			// Deferred / MCP resource tools — Tier-2 (no proven one-to-one AGY role).
			"DeferredToolPlaceholder": "wp_resolve_tool",
			"WaitForMcpServers":       "wp_wait_integrations",
			"ListMcpResourcesTool":    "wp_list_resources",
			"ReadMcpResourceTool":     "wp_read_resource",
			"ReadMcpResourceDirTool":  "wp_list_resource_dir",
		},
		NewsURLSanitization: &spec.NewsURLSanitization{
			SourceURL:      NewsURL,
			ReplacementURL: NewsURLSanitized,
			SentenceForms: []string{
				NewsSentencePrefix + "Claude can direct them to " + NewsURL + " for more information.",
				NewsSentencePrefix + "Antigravity can direct them to " + NewsURL + " for more information.",
			},
		},
		AgentTier: &spec.AgentTierConfig{
			ToolSourceName:         AgentToolSourceName,
			ModelDescriptionPrefix: AgentModelDescriptionPrefix,
			TierAliases: map[string]string{
				"opus":   "gemini-pro",
				"sonnet": "gemini-flash",
				"haiku":  "gemini-flash-lite",
				"fable":  "gemini-argon",
			},
		},
	}
}
