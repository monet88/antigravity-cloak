package main

import (
	"testing"
	"time"
)

func TestStreamSessionManagerCleanupStalePreservesProtectedActiveSession(t *testing.T) {
	const requestID = "cleanup-protected-active"
	globalLifecycleManager.setRoute(requestID, &explicitOMPRouteState{
		routeKind: routeKindProtectedAGY,
		client:    "oh_my_pi",
	})
	defer globalLifecycleManager.deleteRoute(requestID)

	mgr := newStreamSessionManager()
	mgr.sessions["req:"+requestID] = &streamSession{
		client:         "oh_my_pi",
		updatedAt:      time.Now().Add(-10 * time.Minute),
		payloadStarted: true,
	}

	mgr.mu.Lock()
	mgr.cleanupStaleLocked()
	_, exists := mgr.sessions["req:"+requestID]
	mgr.mu.Unlock()
	if !exists {
		t.Fatal("expected stale ProtectedAGY session with active payload state to survive cleanup")
	}
}
