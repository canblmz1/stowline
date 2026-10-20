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

// agentPinHandler is the heartbeat's update decision, including health
// confirmation. stage returns true only after a guarded swap is ready to restart.
type agentPinHandler struct {
	activity   *upgradeGate
	exePath    string
	currentSHA func(string) (string, error)
	stage      func(exePath, currentSHA, wantSHA string, allowed bool) bool
}

func (h agentPinHandler) onPin(wantSHA string, qualified, canaryAllowed bool) {
	// Every successful heartbeat confirms the running build, even when it
	// carries no update or work currently prevents a permitted update.
	confirmUpgradeHealthy(h.exePath)
	allowed := qualified || canaryAllowed
	if !allowed || wantSHA == "" {
		return
	}
	currentSHA, err := h.currentSHA(h.exePath)
	if err != nil || currentSHA == wantSHA {
		return
	}
	// Comparing the pin must not reserve a restart. An operation may have
	// started during hashing; beginUpgrade rechecks admission atomically.
	if !h.activity.beginUpgrade() {
		return
	}
	staged := false
	defer func() {
		if !staged {
			h.activity.cancelUpgrade()
		}
	}()
	staged = h.stage(h.exePath, currentSHA, wantSHA, allowed)
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
