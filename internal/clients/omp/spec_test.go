package omp

import (
	"strings"
	"testing"
)

func TestSpecInvariants(t *testing.T) {
	s := Spec()
	if s.ID != ClientID {
		t.Fatalf("ID = %q, want %q", s.ID, ClientID)
	}
	if !s.HasProtectedRoute {
		t.Fatalf("HasProtectedRoute = false, want true")
	}
	if len(s.DefaultCloakTools) != 9 {
		t.Fatalf("len(DefaultCloakTools) = %d, want 9", len(s.DefaultCloakTools))
	}
	if len(s.CloakedTargetTools) != 9 {
		t.Fatalf("len(CloakedTargetTools) = %d, want 9", len(s.CloakedTargetTools))
	}
	for src, tgt := range s.DefaultCloakTools {
		if !s.CloakedTargetTools[tgt] {
			t.Errorf("DefaultCloakTools[%q] = %q missing from CloakedTargetTools", src, tgt)
		}
	}
	if len(s.ProtectedReverseBrand) != 7 {
		t.Fatalf("len(ProtectedReverseBrand) = %d, want 7", len(s.ProtectedReverseBrand))
	}
	if len(s.PromptSanitizations) != 17 {
		t.Fatalf("len(PromptSanitizations) = %d, want 17", len(s.PromptSanitizations))
	}
	seenSanitizations := make(map[string]bool, len(s.PromptSanitizations))
	for i, p := range s.PromptSanitizations {
		if strings.TrimSpace(p.Match) == "" {
			t.Errorf("PromptSanitizations[%d] has empty Match", i)
		}
		key := strings.ToLower(p.Match)
		if seenSanitizations[key] {
			t.Errorf("PromptSanitizations[%d] duplicates case-insensitive Match %q", i, p.Match)
		}
		seenSanitizations[key] = true
	}
}
