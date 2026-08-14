package process

import (
	"time"

	"github.com/qleelulu/procmesh/internal/health"
)

// SeedInstanceMemory plants restart/health bookkeeping for tests.
func SeedInstanceMemory(m *Manager, id string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[id] = []time.Time{now}
	m.nextTry[id] = now.Add(time.Hour)
	m.healthTrackers[id] = health.NewTracker(HealthCheckSpec{})
	m.lastHealthCheck[id] = now
	m.lastHealthRestart[id] = now
}

// InstanceMemoryHeld reports whether any per-instance maps still mention id.
func InstanceMemoryHeld(m *Manager, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.failures[id]; ok {
		return true
	}
	if _, ok := m.nextTry[id]; ok {
		return true
	}
	if _, ok := m.healthTrackers[id]; ok {
		return true
	}
	if _, ok := m.lastHealthCheck[id]; ok {
		return true
	}
	if _, ok := m.lastHealthRestart[id]; ok {
		return true
	}
	if _, ok := m.clients[id]; ok {
		return true
	}
	return false
}
