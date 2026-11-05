package gateway

import (
	"testing"
	"time"
)

func TestSessionStatusUsesObservedTimesAndPausesConnectedExpiry(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	inst := &instance{startedAt: now.Add(-time.Hour), lastUsed: now, ephemeral: true, idle: 15 * time.Minute}
	m := &manager{instances: map[string]*instance{"test": inst}, idle: 15 * time.Minute}
	status := m.withActivity(sessionInfo{ID: "test", Running: true}, Policy{})
	if status.StartedAt != inst.startedAt.UTC().Format(time.RFC3339) || status.LastActivity != now.UTC().Format(time.RFC3339) || status.IdleExpiresAt != now.Add(15*time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("bad status: %+v", status)
	}
	inst.conns = 1
	if status := m.withActivity(sessionInfo{ID: "test", Running: true}, Policy{}); status.IdleExpiresAt != "" {
		t.Fatal("connected session expires")
	}
	inst.startedAt = time.Time{}
	if status := m.withActivity(sessionInfo{ID: "test", Running: true}, Policy{}); status.StartedAt != "" {
		t.Fatal("adopted start time fabricated")
	}
	if status := m.withActivity(sessionInfo{ID: "test", Running: false}, Policy{}); status.LastActivity != "" {
		t.Fatal("stopped session has live activity")
	}
}
