package codex

import "testing"

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
	if len(s.DefaultCloakTools) != 4 {
		t.Fatalf("len(DefaultCloakTools) = %d, want 4", len(s.DefaultCloakTools))
	}
	targets := make(map[string]bool, len(s.DefaultCloakTools))
	for _, tgt := range s.DefaultCloakTools {
		if targets[tgt] {
			t.Errorf("duplicate DefaultCloakTools target %q", tgt)
		}
		targets[tgt] = true
	}
	if len(s.SourceIdentityTools) != 19 {
		t.Fatalf("len(SourceIdentityTools) = %d, want 19", len(s.SourceIdentityTools))
	}
}
