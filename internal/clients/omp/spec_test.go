package omp

import "testing"

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
	if len(s.PromptSanitizations) != 5 {
		t.Fatalf("len(PromptSanitizations) = %d, want 5", len(s.PromptSanitizations))
	}
}
