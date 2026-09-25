package application

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

func restoreReq(t *testing.T, job domain.JobID, snap string) domain.RestoreRequest {
	t.Helper()
	if snap == "" {
		snap = strings.Repeat("a", 64)
	}
	return domain.RestoreRequest{
		JobID:       job,
		SnapshotID:  domain.SnapshotID(snap),
		StagingRoot: t.TempDir(),
		Repository:  storage.LocalDescriptor(t.TempDir(), "g", "d"),
	}
}

func TestRestoreIdempotentSameRequest(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &restoreEngine{result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-dup", "")
	first, err := s.Restore(context.Background(), req)
	if err != nil || first.State != domain.RestoreReady {
		t.Fatalf("first: %v %+v", err, first)
	}
	second, err := s.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if eng.okEngine.called != 0 && second.State != domain.RestoreReady {
		t.Fatalf("second: %+v", second)
	}
	if second.State != domain.RestoreReady {
		t.Fatalf("duplicate after READY must reconstruct READY, got %s", second.State)
	}
}

func TestRestoreConflictOnChangedSnapshot(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &restoreEngine{result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-conflict", strings.Repeat("a", 64))
	if _, err := s.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.SnapshotID = domain.SnapshotID(strings.Repeat("b", 64))
	_, err = s.Restore(context.Background(), req)
	if !errors.Is(err, domain.ErrRestoreConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestRestoreConflictOnChangedSelection(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &restoreEngine{result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-sel", strings.Repeat("c", 64))
	req.Selections = []string{"docs"}
	if _, err := s.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Selections = []string{"other"}
	_, err = s.Restore(context.Background(), req)
	if !errors.Is(err, domain.ErrRestoreConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestRestoreDuplicateAfterFailed(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &restoreEngine{result: domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorIntegrity}}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-fail", strings.Repeat("d", 64))
	first, err := s.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.State == domain.RestoreReady {
		t.Fatal("failed restore marked READY")
	}
	eng.result = domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}
	second, err := s.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.State == domain.RestoreReady {
		t.Fatal("duplicate after FAILED must not relaunch")
	}
}

func TestRestoreDuplicateWhileRunning(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	eng := &blockRestoreEngine{started: started, release: release}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-run", strings.Repeat("e", 64))
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Restore(context.Background(), req)
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not start")
	}
	second, err := s.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != domain.RestoreRestoring {
		t.Fatalf("in-flight duplicate must return RESTORING, got %s", second.State)
	}
	close(release)
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("first restore hung")
	}
}

func TestRestoreReconcileThenDuplicate(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &restoreEngine{result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}}
	s := &Service{Engine: eng, Journal: j}
	req := restoreReq(t, "restore-recon", strings.Repeat("f", 64))
	if _, err := s.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(context.Background(), req.Repository, req.PasswordRef); err != nil {
		t.Fatal(err)
	}
	second, err := s.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != domain.RestoreReady {
		t.Fatalf("after reconcile, same request must remain READY, got %s", second.State)
	}
}

type blockRestoreEngine struct {
	okEngine
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockRestoreEngine) Restore(ctx context.Context, req domain.RestoreRequest) (domain.RestoreResult, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
	case <-ctx.Done():
	}
	return domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}, nil
}
