package codex

import (
	"slices"

	"github.com/monet88/antigravity-cloak/internal/spec"
)

// ClientID is the canonical wire identifier for OpenAI Codex.
const ClientID = "codex"

// IdentityLine is the opening sentence the Codex client really ships,
// verified verbatim in
// .ref/CLIProxyAPI/internal/registry/models/codex_client_models.json:
// "You are Codex, an agent based on GPT-6. You and the user share one
// workspace, ...". It is replaced whole, the way the Claude Code and Oh My Pi
// identities are, because token substitution alone leaves a sentence that names
// neither this product nor its vendor. Longer Codex identity prose after it
// (including the OpenAI spelling some builds ship) is handled by the residual
// rules below, so this stays a sentence rule and not a whole-prompt rule.
const IdentityLine = "You are Codex, an agent based on GPT-6."

// GeneralIdentityLine is the second opening sentence the Codex client
// ships, supplied from a real session:
//
//	You are Codex, an OpenAI general-purpose agentic assistant that helps the
//	user complete tasks across coding, browsing, apps, documents, research, and
//	other digital workflows.
//
// It is replaced whole for the same reason as IdentityLine, and it has to be
// a whole-sentence rule rather than residual substitution: "OpenAI" alone would
// turn it into "an Google Deepmind general-purpose agentic assistant", which is
// both grammatically wrong and a sentence no client ever wrote.
const GeneralIdentityLine = "You are Codex, an OpenAI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."

// Spec returns the declarative data-only specification for OpenAI Codex.
func Spec() spec.ClientSpec {
	// Codex keeps its file names, per codex-rs/core/src/agents_md.rs: AGENTS.md is
	// already the neutral name Codex reads, so there is no brand in the file name to
	// hide and none is invented. Only the directory is rewritten.
	contextMappings := spec.PathRules(".codex", ".gemini", "AGENTS.md", "AGENTS.md")
	reverseContextMappings := spec.PathRules(".gemini", ".codex", "AGENTS.md", "AGENTS.md")

	forwardBrandMappings := slices.Concat(
		// Codex keeps its file names; only the home directory is rewritten. See
		// spec.PathRules for why the match is not anchored to a ~/ or ./ spelling.
		contextMappings,
		[]spec.RewriteMapping{
			// Whole identity first, before any shorter token. Both shipped opening
			// sentences are terminal rewrites: the native identity line replaces
			// them whole, so the vendor word inside them never reaches the residual
			// rules and cannot come out as "an Google Deepmind ..." prose.
			{Match: IdentityLine, Replacement: spec.AntigravityIdentity},
			{Match: GeneralIdentityLine, Replacement: spec.AntigravityIdentity},
			// Residual client-scoped prose. Codex's own prompts carry the client
			// name and the model family it is built on ("based on GPT-6"); some
			// builds' longer identity prose names the vendor as OpenAI. Each pair
			// is inverted exactly by reverseBrandMappings, in this same order.
			{Match: "OpenAI", Replacement: "Google Deepmind"},
			{Match: "GPT-6", Replacement: "Gemini 3"},
			// Bare client name, after every longer form above. Its casing follows
			// the text it matched (Codex -> Antigravity, codex -> antigravity,
			// CODEX -> ANTIGRAVITY) via isProseBrandRule, so one rule covers all
			// three spellings and none can shadow another.
			{Match: "Codex", Replacement: "Antigravity"},
		},
	)

	reverseBrandMappings := slices.Concat(
		// The exact inverse of the forward context group. Not a blanket ".gemini/"
		// -> ".codex/": the forward pass never introduced one, so a blanket here
		// would rewrite a ".gemini" the user typed.
		reverseContextMappings,
		[]spec.RewriteMapping{
			// Exact inverses of the residual identity prose, in forward order.
			{Match: "Google Deepmind", Replacement: "OpenAI"},
			{Match: "Gemini 3", Replacement: "GPT-6"},
			// The bare brand word, last so the longer tokens above win the prefix.
			// The exclusion is what keeps the real Antigravity host intact: the dot
			// after "Antigravity" is a non-word byte, so its right boundary passes
			// and this rule would otherwise hand the client "Codex.google", a host
			// that does not exist. Codex's forward pass never introduces that
			// domain, so nothing there has to be restored, and an exclusion is the
			// right shape where a repair rule would have to invent an "omp.google"
			// or "Codex.google" source domain that the contract does not have.
			{Match: "Antigravity", Replacement: "Codex", NotFollowedBy: spec.AntigravityHostPreserve},
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
			// Code mode is the default for every routed provider here, so this table
			// keys on what a code-mode session actually declares.
			"exec":                       "run_command",
			"web_search":                 "search_web",
			"request_user_input":         "ask_question",
			"collaboration__spawn_agent": "invoke_subagent",
		},
		SharedAliases: map[string]string{
			// Previously intentional pass-through, now cloaked to shared aliases.
			"wait":                           "wp_wait",
			"request_user_input_async":       "wp_request_user_input_async",
			"clock__sleep":                   "wp_clock_sleep",
			"collaboration__wait_agent":      "wp_collaboration_wait_agent",
			"collaboration__interrupt_agent": "wp_collaboration_interrupt_agent",
			"collaboration__send_message":    "wp_send_message",
			// Moved from unproven semantic mappings in defaultCloakTables.
			"collaboration__followup_task": "wp_collaboration_followup_task",
			"collaboration__list_agents":   "wp_list_workers",
			// Shell-mode tools that need cloaking. exec_command is the shell-mode
			// entry point (proven semantic match to run_command, same as exec in
			// code mode). Kept here rather than in DefaultCloakTools to avoid a
			// non-injective inverse map at init(); each request must declare or use
			// only one entry point; distinct sources sharing run_command in a single
			// request reject admission.
			"exec_command": "run_command",
			"apply_patch":  "wp_apply_patch",
			"write_stdin":  "wp_write_stdin",
			"view_image":   "wp_view_image",
		},
		SourceIdentityTools: map[string]bool{
			"exec":                           true,
			"exec_command":                   true,
			"write_stdin":                    true,
			"apply_patch":                    true,
			"view_image":                     true,
			"web_search":                     true,
			"request_user_input":             true,
			"request_user_input_async":       true,
			"spawn_agent":                    true,
			"followup_task":                  true,
			"list_agents":                    true,
			"wait_agent":                     true,
			"interrupt_agent":                true,
			"collaboration__spawn_agent":     true,
			"collaboration__followup_task":   true,
			"collaboration__list_agents":     true,
			"collaboration__wait_agent":      true,
			"collaboration__interrupt_agent": true,
			"collaboration__send_message":    true,
		},
	}
}
