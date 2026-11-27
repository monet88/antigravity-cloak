package claudecode

import (
	"strings"
	"testing"
)

func TestSpecInvariants(t *testing.T) {
	s := Spec()
	if s.ID != ClientID {
		t.Fatalf("ID = %q, want %q", s.ID, ClientID)
	}
	if !s.UsesAliasPlan {
		t.Fatalf("UsesAliasPlan = false, want true")
	}
	if len(s.ContextMappings) != len(s.ReverseContextMappings) || len(s.ContextMappings) == 0 {
		t.Fatalf("context group lengths mismatch: fwd=%d rev=%d", len(s.ContextMappings), len(s.ReverseContextMappings))
	}
	for i := range s.ContextMappings {
		if s.ReverseContextMappings[i].Match != s.ContextMappings[i].Replacement ||
			s.ReverseContextMappings[i].Replacement != s.ContextMappings[i].Match {
			t.Errorf("context[%d] not exact inverse", i)
		}
	}
	if len(s.DefaultCloakTools) != 9 {
		t.Fatalf("len(DefaultCloakTools) = %d, want 9", len(s.DefaultCloakTools))
	}
	targets := make(map[string]bool, len(s.DefaultCloakTools))
	for _, tgt := range s.DefaultCloakTools {
		if targets[tgt] {
			t.Errorf("duplicate DefaultCloakTools target %q", tgt)
		}
		targets[tgt] = true
	}
	if s.NewsURLSanitization == nil || len(s.NewsURLSanitization.SentenceForms) != 2 {
		t.Fatalf("NewsURLSanitization = %#v, want 2 sentence forms", s.NewsURLSanitization)
	}
	for i, form := range s.NewsURLSanitization.SentenceForms {
		if !strings.Contains(form, NewsURL) {
			t.Errorf("SentenceForms[%d] missing NewsURL", i)
		}
	}
	if s.AgentTier == nil || len(s.AgentTier.TierAliases) != 4 {
		t.Fatalf("AgentTier = %#v, want 4 tier aliases", s.AgentTier)
	}
	tierTargets := make(map[string]bool, len(s.AgentTier.TierAliases))
	for _, alias := range s.AgentTier.TierAliases {
		if tierTargets[alias] {
			t.Errorf("duplicate tier alias %q", alias)
		}
		tierTargets[alias] = true
	}
}
