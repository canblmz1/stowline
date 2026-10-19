package main

import (
	"sync"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// upgradeGate makes operation admission and the upgrade/restart decision one
// critical section. Once upgrading, no new backup or restore may be admitted.
// The gate stays closed across the binary swap until this process exits.
type upgradeGate struct {
	mu        sync.Mutex
	active    int
	upgrading bool
}

func (g *upgradeGate) beginOperation() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.upgrading {
		return domain.ErrOperationInProgress
	}
	g.active++
	return nil
}

func (g *upgradeGate) endOperation() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
}

func (g *upgradeGate) beginUpgrade() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.upgrading || g.active != 0 {
		return false
	}
	g.upgrading = true
	return true
}

func (g *upgradeGate) cancelUpgrade() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.upgrading = false
}
