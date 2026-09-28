package main

import (
	"net/http"
	"strings"
	"testing"
)

// Oh My Pi's real tool surface must never reach upstream uncloaked. A tool
// name is a client-declared identifier the model calls back by name, so one
// name surviving the forward pass is a leak of the client's own tool inventory.
//
// The list is BUILTIN_TOOL_NAMES + HIDDEN_TOOL_NAMES from
// .ref/oh-my-pi/packages/coding-agent/src/tools/builtin-names.ts, plus the
// names declared in that package's tool definition files. When Oh My Pi gains
// or renames a tool, add it here: a name missing from the list is still safe
// (the deterministic wp_ext_<hash> fallback covers unknown tools), but a name
// that starts leaking would not be caught.
func TestOMPToolSurfaceIsFullyCloaked(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	names := []string{
		"read", "bash", "edit", "ast_grep", "ast_edit", "ask", "debug", "ida",
		"eval", "github", "glob", "grep", "find", "lsp", "checkpoint", "rewind",
		"context_notes", "new_context", "security_scan", "task", "wait", "todo",
		"web_search", "write", "memory_edit", "retain", "recall", "reflect",
		"learn", "manage_skill",
		"yield", "goal", "think",
		"browser", "computer", "origin", "generate_image", "tts",
	}

	tools := make([]string, 0, len(names))
	for _, n := range names {
		tools = append(tools, `{"type":"function","function":{"name":"`+n+`"}}`)
	}
	body := []byte(`{"system":"x","messages":[{"role":"user","content":"x"}],"tools":[` + strings.Join(tools, ",") + `]}`)

	h := http.Header{}
	h.Set("X-Cloak-Client", "oh_my_pi")
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "req_omp_tools", "openai", "agy/m", body, h))
	if code != 0 {
		t.Fatalf("code=%d %s", code, raw)
	}
	upstream := string(decodeEnvelopeBody(t, raw))

	for _, n := range names {
		if strings.Contains(upstream, `"name":"`+n+`"`) {
			t.Errorf("tool %q reached upstream uncloaked", n)
		}
	}
	if !strings.Contains(upstream, `"name":"view_file"`) || !strings.Contains(upstream, `"name":"run_command"`) {
		t.Errorf("canonical tools did not reach the shared aliases: %s", upstream)
	}
}
