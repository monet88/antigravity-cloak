package omp

import "github.com/monet88/antigravity-cloak/internal/spec"

// ClientID is the canonical wire identifier for Oh My Pi.
const ClientID = "oh_my_pi"

// SchemePreserve is the client's own virtual-device scheme, an operational
// address rather than brand prose. A rule that masks the bare alias carries it
// as an exclusion, so "omp://xd/mcp__server_tool" reaches the model
// byte-for-byte while the same word in prose is still masked.
const SchemePreserve = "://"

// ReverseBrandMatch is the bare brand token inverted on the protected OMP route.
const ReverseBrandMatch = "Antigravity"

// ReverseBrandReplacement is the restored bare brand token for Oh My Pi.
const ReverseBrandReplacement = "omp"

// RFC2119KeywordsReplacement is the normalized opening clause replacing OMP's RFC 2119 headers.
const RFC2119KeywordsReplacement = "Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."

// ClaudeCodeOAuthInstruction is the injected OAuth instruction emitted by omp/18.20.0+
// for custom anthropic-messages models without an explicit auth field.
const ClaudeCodeOAuthInstruction = "You are Claude Code, Anthropic's official CLI for Claude."

// Spec returns the declarative data-only specification for Oh My Pi.
func Spec() spec.ClientSpec {
	identityLines := []spec.RewriteMapping{
		{Match: "You are omp's trusted coding assistant.", Replacement: spec.AntigravityIdentity},
	}

	return spec.ClientSpec{
		ID:                    ClientID,
		ExplicitAliases:       []string{"oh_my_pi", "omp", "oh-my-pi"},
		UserAgentPrefixes:     []string{"omp/"},
		HasProtectedRoute:     true,
		SchemePreserve:        SchemePreserve,
		IdentityLines:         identityLines,
		MandatoryBrandAliases: []string{"Oh My Pi", "oh-my-pi", "omp"},
		ForwardBrandMappings: []spec.RewriteMapping{
			// Same whole-sentence rewrite as the protected route, for an OMP marker
			// that did not take the protected branch.
			identityLines[0],
			// Oh My Pi reads the user's GLOBAL Claude memory, ~/.claude/CLAUDE.md
			// (discovery/claude.ts:65-69,163-188), while its own root context file is a
			// neutral AGENTS.md (discovery/agents-md.ts:21). That global file exists on
			// disk, so its path is a real operational identifier on this route. Only
			// this one file is remapped, in both spellings plus the JSON-escaped one,
			// and as a whole path element: the .claude directory itself stays verbatim,
			// because a blanket .claude -> .gemini would collide with .omp -> .gemini
			// and make the reverse ambiguous. There is no project-local
			// .claude/CLAUDE.md in play, so nothing else is claimed.
			// ProtectedReverseBrand inverts this pair.
			{Match: ".claude/CLAUDE.md", Replacement: ".gemini/AGENTS.md", WholeSegment: true},
			{Match: `.claude\CLAUDE.md`, Replacement: `.gemini\AGENTS.md`, WholeSegment: true},
			{Match: `.claude\\CLAUDE.md`, Replacement: `.gemini\\AGENTS.md`, WholeSegment: true},
			// Oh My Pi coding agent & harness. On the protected route these go through
			// the sentinel path instead; these entries cover an OMP marker that did not
			// take the protected branch. The bare alias carries the scheme exclusion so
			// a real "omp://" internal URI stays byte-for-byte.
			{Match: "Oh My Pi", Replacement: "Antigravity"},
			{Match: "oh-my-pi", Replacement: "Antigravity"},
			{Match: "omp", Replacement: "Antigravity", NotFollowedBy: SchemePreserve},
		},
		ProtectedReverseBrand: []spec.RewriteMapping{
			// The protected pair owns the bare brand word. Its exclusion is what keeps
			// the real Antigravity host intact: the dot after "Antigravity" is a
			// non-word byte, so the rule's right boundary passes inside
			// "antigravity.google" and it would otherwise hand the client the dead host
			// "omp.google".
			{Match: ReverseBrandMatch, Replacement: ReverseBrandReplacement, NotFollowedBy: spec.AntigravityHostPreserve},
			// Must precede the .gemini rules below, which would otherwise consume the
			// directory first and leave "AGENTS.md" attached to the wrong root.
			{Match: ".gemini/AGENTS.md", Replacement: ".claude/CLAUDE.md", WholeSegment: true, ArgumentSafe: true},
			{Match: `.gemini\AGENTS.md`, Replacement: `.claude\CLAUDE.md`, WholeSegment: true, ArgumentSafe: true},
			{Match: `.gemini\\AGENTS.md`, Replacement: `.claude\\CLAUDE.md`, WholeSegment: true, ArgumentSafe: true},
			{Match: ".gemini/", Replacement: ".omp/", WholeSegment: true, ArgumentSafe: true},
			{Match: `.gemini\`, Replacement: `.omp\`, WholeSegment: true, ArgumentSafe: true},
			// The bare form, matching the forward remap's end-of-string case.
			{Match: ".gemini", Replacement: ".omp", WholeSegment: true, SegmentEnd: true, ArgumentSafe: true},
		},
		DefaultCloakTools: map[string]string{
			"read":       "read_file",
			"write":      "write_file",
			"edit":       "edit_file",
			"bash":       "run_command",
			"grep":       "grep_search",
			"glob":       "find_by_name",
			"task":       "invoke_subagent",
			"ask":        "ask_question",
			"web_search": "search_web",
		},
		SharedAliases: map[string]string{
			// Standard pass-through tools.
			"todo": "wp_todo", "hub": "wp_hub", "eval": "wp_eval",
			// Vibe Mode.
			"vibe_spawn": "wp_vibe_spawn", "vibe_send": "wp_vibe_send",
			"vibe_wait": "wp_vibe_wait", "vibe_kill": "wp_vibe_kill", "vibe_list": "wp_vibe_list",
			// Autoresearch Mode.
			"init_experiment": "wp_init_experiment", "run_experiment": "wp_run_experiment",
			"log_experiment": "wp_log_experiment", "update_notes": "wp_update_notes",
			// Memory & skill (no AGY equivalent).
			"learn": "wp_learn", "manage_skill": "wp_manage_skill",
			// Semantic search (cannot map to grep_search: declaration collision).
			"find": "wp_find",
			// Goal and loop runtime.
			"goal": "wp_goal", "yield": "wp_yield", "wait": "wp_wait",
		},
		SourceIdentityTools: map[string]bool{
			"read":            true,
			"write":           true,
			"edit":            true,
			"bash":            true,
			"grep":            true,
			"glob":            true,
			"task":            true,
			"ask":             true,
			"web_search":      true,
			"todo":            true,
			"hub":             true,
			"eval":            true,
			"vibe_spawn":      true,
			"vibe_send":       true,
			"vibe_wait":       true,
			"vibe_kill":       true,
			"vibe_list":       true,
			"init_experiment": true,
			"run_experiment":  true,
			"log_experiment":  true,
			"update_notes":    true,
		},
		CloakedTargetTools: map[string]bool{
			"read_file":       true,
			"write_file":      true,
			"edit_file":       true,
			"run_command":     true,
			"grep_search":     true,
			"find_by_name":    true,
			"invoke_subagent": true,
			"ask_question":    true,
			"search_web":      true,
		},
		DistinctiveTools: map[string]bool{
			"hub": true, "task": true, "todo": true, "eval": true, "web_search": true,
			"vibe_spawn": true, "vibe_send": true, "vibe_wait": true, "vibe_kill": true, "vibe_list": true,
			"init_experiment": true, "run_experiment": true, "log_experiment": true, "update_notes": true,
		},
		PromptSanitizations: []spec.PromptSanitizationPreset{
			{Match: "<system-conventions>", Replacement: "<conventions>", CaseSensitive: true},
			{Match: "</system-conventions>", Replacement: "</conventions>", CaseSensitive: true},
			{Match: "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.", Replacement: RFC2119KeywordsReplacement, CaseSensitive: false},
			{Match: "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.", Replacement: RFC2119KeywordsReplacement, CaseSensitive: false},
			{Match: ClaudeCodeOAuthInstruction, Replacement: spec.AntigravityIdentity, CaseSensitive: false},
		},
	}
}
