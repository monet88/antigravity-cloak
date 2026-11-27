package spec

import "strings"

// AntigravityIdentity is the opening <identity> line the real Antigravity CLI
// sends, captured verbatim from a native session's system prompt. The main
// session and its subagents share it.
const AntigravityIdentity = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding."

// AntigravityHostPreserve is the native Antigravity host, a real operational
// domain rather than brand prose. The bare "Antigravity" reverse rule carries
// it as an exclusion: the dot after the word is a non-word byte, so the rule's
// right boundary passes inside "antigravity.google" and would otherwise hand
// the client a host that does not exist ("omp.google", "Codex.google"). Naming
// the exclusion is also what makes the reverse chunk-safe - a lane holds the
// word while this suffix is still arriving instead of resolving it early.
const AntigravityHostPreserve = ".google"

// RewriteMapping declares a single brand or operational-identifier rewrite rule.
type RewriteMapping struct {
	Match       string
	Replacement string
	// Client scopes the rule to one resolved client id (e.g. "claude_code").
	// Empty applies the rule to every client.
	Client string
	// WholeSegment requires the match to begin a whole path element: the byte
	// before it may not be a word byte, so ".claude/" is not a match inside
	// "foo.claude/settings.json". Set on the path groups (PathRules and the
	// client path pairs), where the match begins with a dot and the generic
	// word-boundary test cannot see the element it is glued to. Left off prose
	// rules, which are matched wherever their own word boundary allows.
	WholeSegment bool
	// SegmentEnd requires the match to END a whole path element: the byte after
	// it must be end of string or a path separator. It is the right-hand half of
	// WholeSegment, and the reason the bare ".gemini" reverse rule cannot hand
	// the client ".omp-backup": the generic word boundary accepts a dot or a
	// hyphen after the match, so ".gemini-backup" and ".gemini.foo" would be
	// rewritten into paths the forward pass deliberately never produced. Only
	// the bare form needs it - a rule that ends in "/" or "\" already forces its
	// own right boundary.
	SegmentEnd bool
	// NotFollowedBy is an exclusion suffix: the rule does not apply when the
	// bytes immediately after the match begin with this string. It is how a
	// rule names a spelling that must survive it untouched - the OMP scheme
	// "omp://" on the forward pass, and the real Antigravity host
	// "antigravity.google" on the reverse pass, where the bare brand rule would
	// otherwise hand the client "omp.google". Chunk-safe: the stream lanes hold
	// a complete match whose exclusion suffix is still arriving rather than
	// resolving it early.
	NotFollowedBy string
	// ArgumentSafe marks a rule the reverse pass may also run over a
	// tool-argument carrier (`input_json_delta.partial_json`,
	// `tool_use.input`, `tool_calls[].function.arguments`).
	//
	// An argument is DATA the client executes or writes to disk, not text it
	// displays, so only an operational identifier the model can legitimately
	// echo from the cloaked prompt belongs there: a path, an SDK or domain name,
	// a client-declared skill or tool identifier. A bare prose brand word does
	// not: the forward pass never writes one into an argument (it rewrites
	// prompts, never tool arguments), so a blind reverse only corrupts literals
	// the model copied from the user - `cd antigravity-cloak && go test ./...`
	// came back as `cd Claude-cloak` and the client ran a directory that does not
	// exist. Prose rules stay prose: they reverse assistant text, and this flag
	// keeps them out of the argument carriers.
	ArgumentSafe bool
}

// PathRules builds one direction of a client context group. The home directory
// is matched as a whole segment at ANY position, not only in a ~/ or ./
// spelling. That is not a preference: live acceptance measured 114 absolute
// paths per request going to the model uncloaked, because Claude Code on
// Windows puts C:\Users\<name>\.claude\transcripts and friends in its system
// context and never uses the tilde form at all. A home-prefixed match misses
// every one of them.
//
// Both pairs are passed in MATCH-then-REPLACE order, so a reverse table is this
// same call with both pairs swapped, and TestContextGroupsAreExactInverses is
// what holds that true.
//
// Every rule is WholeSegment, so the directory has to begin a whole path
// element: "foo.claude/settings.json" and "foo.codex/config.toml" are not
// matches and are handed through byte-for-byte. The dot itself is not a word
// byte, so the generic word-boundary test cannot see the element a ".claude" is
// glued to; this flag is that test.
//
// Each separator spelling gets two rules, and the order is load-bearing three
// deep: the file rule must precede its own directory rule so its longer match
// wins, and the JSON-ESCAPED file rule must precede the plain "\" directory
// rule. A path inside a tool call's arguments arrives as JSON text, not as
// decoded prose - the model escapes every backslash when it writes the path
// into OpenAI function.arguments or Anthropic partial_json - so the plain
// directory rule would consume the directory, leave the file name behind, and
// hand the client ".claude\\GEMINI.md": half its file name, half ours. The
// escaped directory rule is not needed, because the plain directory rule
// already leaves the second backslash of the pair in place.
func PathRules(fromDir, toDir, fromFile, toFile string) []RewriteMapping {
	fileRule := func(sep string) RewriteMapping {
		return RewriteMapping{
			Match:        fromDir + sep + strings.ReplaceAll(fromFile, "/", sep),
			Replacement:  toDir + sep + strings.ReplaceAll(toFile, "/", sep),
			WholeSegment: true,
			// A path is what the model actually writes into a command or a
			// file, so every rule of this group runs over tool arguments too.
			ArgumentSafe: true,
		}
	}
	dirRule := func(sep string) RewriteMapping {
		return RewriteMapping{Match: fromDir + sep, Replacement: toDir + sep, WholeSegment: true, ArgumentSafe: true}
	}
	return []RewriteMapping{
		fileRule("/"),
		dirRule("/"),
		fileRule(`\`),
		// The escaped spelling of fileRule(`\`): JSON writes each backslash
		// twice, and JSON never escapes a forward slash, so the "/" rules above
		// need no twin.
		fileRule(`\\`),
		dirRule(`\`),
	}
}

// PromptSanitizationPreset declares a built-in inbound prompt sanitization rule.
type PromptSanitizationPreset struct {
	Match         string
	Replacement   string
	CaseSensitive bool
}

// NewsURLSanitization declares static data for sanitizing a client's known system news sentence.
type NewsURLSanitization struct {
	SourceURL      string
	ReplacementURL string
	SentenceForms  []string
}

// AgentTierConfig declares static data for reversible subagent model tier translation.
type AgentTierConfig struct {
	ToolSourceName         string
	ModelDescriptionPrefix string
	TierAliases            map[string]string
}

// ClientSpec is the data-only declarative specification of a coding client.
type ClientSpec struct {
	ID                     string
	ExplicitAliases        []string
	UserAgentPrefixes      []string
	UsesAliasPlan          bool
	HasProtectedRoute      bool
	SchemePreserve         string
	IdentityLines          []RewriteMapping
	MandatoryBrandAliases  []string
	ForwardBrandMappings   []RewriteMapping
	ReverseBrandMappings   []RewriteMapping
	ProtectedReverseBrand  []RewriteMapping
	ContextMappings        []RewriteMapping
	ReverseContextMappings []RewriteMapping
	DefaultCloakTools      map[string]string
	SharedAliases          map[string]string
	SourceIdentityTools    map[string]bool
	CloakedTargetTools     map[string]bool
	DistinctiveTools       map[string]bool
	PromptSanitizations    []PromptSanitizationPreset
	NewsURLSanitization    *NewsURLSanitization
	AgentTier              *AgentTierConfig
}
