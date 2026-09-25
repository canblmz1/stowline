package application

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Regression: Reconcile runs on every scheduler tick. An interrupted restore
// (or any non-backup) attempt left in the journal must not make Reconcile
// return an error, because that error propagates through Controller.Tick and
// silently blocks every future scheduled backup.
func TestReconcileDoesNotBlockSchedulerOnInterruptedRestore(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()

	// Restore that persisted intent then crashed before CompleteAttempt.
	if err := j.BeginJob(ctx,
		ports.JobRecord{ID: "r-job", Kind: domain.JobRestore, State: domain.JobActive},
		ports.AttemptRecord{ID: "r-att", JobID: "r-job", AttemptNo: 1, Kind: domain.JobRestore, Phase: domain.PhasePreparing},
	); err != nil {
		t.Fatal(err)
	}
	// A genuinely interrupted backup alongside it, to prove backup reconciliation
	// still runs after the non-backup entry.
	if err := j.BeginJob(ctx,
		ports.JobRecord{ID: "b-job", Kind: domain.JobBackup, SlotKey: "slot-1", State: domain.JobActive},
		ports.AttemptRecord{ID: "b-att", JobID: "b-job", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhaseBackingUp},
	); err != nil {
		t.Fatal(err)
	}

	s := &Service{Engine: &okEngine{}, Journal: j}
	done, err := s.Reconcile(ctx, storage.LocalDescriptor(t.TempDir(), "g", "d"), domain.SecretRef{Locator: "pw"})
	if err != nil {
		t.Fatalf("Reconcile must not error on an interrupted restore: %v", err)
	}
	if len(done) != 2 {
		t.Fatalf("both attempts should be resolved, got %d", len(done))
	}

	// The interrupted restore is now terminal (FAILED), not stuck.
	rAtt, err := j.GetAttempt(ctx, "r-att")
	if err != nil {
		t.Fatal(err)
	}
	if !rAtt.Phase.Terminal() || rAtt.Phase != domain.PhaseFailed {
		t.Fatalf("interrupted restore left in phase %q", rAtt.Phase)
	}

	// A second reconcile is a clean no-op (nothing incomplete remains).
	done2, err := s.Reconcile(ctx, storage.LocalDescriptor(t.TempDir(), "g", "d"), domain.SecretRef{Locator: "pw"})
	if err != nil || len(done2) != 0 {
		t.Fatalf("second reconcile not clean: done=%d err=%v", len(done2), err)
	}
}
