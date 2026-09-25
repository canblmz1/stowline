package application

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

type failJournal struct{}

func (failJournal) Open() error  { return nil }
func (failJournal) Close() error { return nil }
func (failJournal) BeginJob(ctx context.Context, job ports.JobRecord, attempt ports.AttemptRecord) error {
	return domain.ErrIntentNotPersisted
}
func (failJournal) UpdateAttempt(context.Context, ports.AttemptRecord) error { return nil }
func (failJournal) CompleteAttempt(context.Context, ports.AttemptRecord, domain.JobState) error {
	return nil
}
func (failJournal) GetAttempt(context.Context, domain.AttemptID) (*ports.AttemptRecord, error) {
	return nil, nil
}
func (failJournal) GetJob(context.Context, domain.JobID) (*ports.JobRecord, error) { return nil, nil }
func (failJournal) JobBySlot(context.Context, string) (*ports.JobRecord, error)    { return nil, nil }
func (failJournal) IncompleteAttempts(context.Context) ([]ports.AttemptRecord, error) {
	return nil, nil
}
func (failJournal) ConsumeCommand(context.Context, string, string, bool) (bool, error) {
	return false, nil
}
func (failJournal) CommandConsumed(context.Context, string) (bool, error) { return false, nil }
func (failJournal) CommandResult(context.Context, string) (bool, string, map[string]any, error) {
	return false, "", nil, nil
}
func (failJournal) FinishCommand(context.Context, string, string, map[string]any) error { return nil }
func (failJournal) AppendOutbox(context.Context, ports.OutboxEvent) error               { return nil }
func (failJournal) UnackedOutbox(context.Context, int) ([]ports.OutboxEvent, error) {
	return nil, nil
}
func (failJournal) AckOutbox(context.Context, string) error              { return nil }
func (failJournal) SavePolicy(context.Context, domain.LocalPolicy) error { return nil }
func (failJournal) LatestPolicy(context.Context) (*domain.LocalPolicy, error) {
	return nil, nil
}
func (failJournal) LatestSucceededBackupSlot(context.Context) (string, error) { return "", nil }
func (failJournal) LatestAttempt(context.Context, domain.JobID) (*ports.AttemptRecord, error) {
	return nil, nil
}
func (failJournal) RecentAttempts(context.Context, int) ([]ports.AttemptRecord, error) {
	return nil, nil
}
func (failJournal) BeginAttempt(context.Context, ports.AttemptRecord) error { return nil }
func (failJournal) SetJobState(context.Context, domain.JobID, domain.JobState) error {
	return nil
}

type boomEngine struct{ called bool }

func (e *boomEngine) Version(context.Context) (string, error) { return "restic 0.19.1", nil }
func (e *boomEngine) Init(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *boomEngine) Backup(context.Context, domain.BackupRequest) (domain.BackupResult, error) {
	e.called = true
	return domain.BackupResult{Outcome: domain.PhaseSucceeded}, nil
}
func (e *boomEngine) Snapshots(context.Context, domain.RepositoryDescriptor, domain.SecretRef) ([]ports.Snapshot, error) {
	return nil, nil
}
func (e *boomEngine) Check(context.Context, domain.RepositoryDescriptor, domain.SecretRef, bool) (ports.CheckResult, error) {
	return ports.CheckResult{}, nil
}
func (e *boomEngine) Restore(context.Context, domain.RestoreRequest) (domain.RestoreResult, error) {
	return domain.RestoreResult{}, nil
}
func (e *boomEngine) CatConfig(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *boomEngine) ReadSnapshot(context.Context, domain.RepositoryDescriptor, domain.SecretRef, domain.SnapshotID) (*ports.Snapshot, error) {
	return nil, nil
}

func TestNoLaunchIfJournalFails(t *testing.T) {
	eng := &boomEngine{}
	s := &Service{Engine: eng, Journal: failJournal{}}
	_, err := s.Backup(context.Background(), domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
	})
	if err == nil || !errors.Is(err, domain.ErrIntentNotPersisted) {
		t.Fatalf("want intent error, got %v", err)
	}
	if eng.called {
		t.Fatal("engine launched without durable intent")
	}
}

func TestEvaluateVSSRequiredWithoutEvidenceIsNotSuccess(t *testing.T) {
	req := domain.BackupRequest{VSSMode: domain.VSSRequired}
	res := domain.BackupResult{
		Outcome:     domain.PhaseSucceeded,
		SnapshotID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReadbackOK:  true,
		ExitKnown:   true,
		Consistency: domain.ConsistencyNotMet,
		Summary:     domain.BackupSummary{MessageSeen: true, SnapshotID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	out := evaluateBackup(req, res)
	if out.Outcome == domain.PhaseSucceeded {
		t.Fatal("VSS required without evidence must not be SUCCEEDED")
	}
}

func TestEvaluatePartialExitNeverGreen(t *testing.T) {
	req := domain.BackupRequest{VSSMode: domain.VSSDisabled}
	res := domain.BackupResult{
		Outcome:     domain.PhasePartial,
		SnapshotID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReadbackOK:  true,
		ExitKnown:   true,
		Consistency: domain.LiveReadAllowed,
		Summary:     domain.BackupSummary{MessageSeen: true},
	}
	out := evaluateBackup(req, res)
	if out.Outcome == domain.PhaseSucceeded {
		t.Fatal("partial must not become success")
	}
}

func TestEvaluateUnknownExitNeverGreen(t *testing.T) {
	req := domain.BackupRequest{VSSMode: domain.VSSDisabled}
	res := domain.BackupResult{
		Outcome:     domain.PhaseSucceeded,
		SnapshotID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReadbackOK:  true,
		ExitKnown:   false,
		ExitCode:    0,
		Consistency: domain.LiveReadAllowed,
		Summary:     domain.BackupSummary{MessageSeen: true},
	}
	out := evaluateBackup(req, res)
	if out.Outcome == domain.PhaseSucceeded {
		t.Fatal("unknown exit must not be SUCCEEDED")
	}
}

func TestEvaluateMissingSummaryFails(t *testing.T) {
	req := domain.BackupRequest{VSSMode: domain.VSSDisabled}
	res := domain.BackupResult{
		Outcome:     domain.PhaseSucceeded,
		SnapshotID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReadbackOK:  true,
		ExitKnown:   true,
		Consistency: domain.LiveReadAllowed,
	}
	out := evaluateBackup(req, res)
	if out.Outcome != domain.PhaseFailed {
		t.Fatalf("%s", out.Outcome)
	}
}

type okEngine struct{ called int }

func (e *okEngine) Version(context.Context) (string, error) { return "restic 0.19.1", nil }
func (e *okEngine) Init(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *okEngine) Backup(context.Context, domain.BackupRequest) (domain.BackupResult, error) {
	e.called++
	id := domain.SnapshotID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	return domain.BackupResult{
		Outcome:     domain.PhaseSucceeded,
		SnapshotID:  id,
		ReadbackOK:  true,
		ExitKnown:   true,
		Consistency: domain.LiveReadAllowed,
		Summary:     domain.BackupSummary{MessageSeen: true, SnapshotID: string(id)},
		CleanupOK:   true,
	}, nil
}
func (e *okEngine) Snapshots(context.Context, domain.RepositoryDescriptor, domain.SecretRef) ([]ports.Snapshot, error) {
	return nil, nil
}
func (e *okEngine) Check(context.Context, domain.RepositoryDescriptor, domain.SecretRef, bool) (ports.CheckResult, error) {
	return ports.CheckResult{}, nil
}
func (e *okEngine) Restore(context.Context, domain.RestoreRequest) (domain.RestoreResult, error) {
	return domain.RestoreResult{}, nil
}
func (e *okEngine) CatConfig(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *okEngine) ReadSnapshot(context.Context, domain.RepositoryDescriptor, domain.SecretRef, domain.SnapshotID) (*ports.Snapshot, error) {
	return nil, nil
}

func TestSlotSkipDoesNotRelaunch(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &okEngine{}
	s := &Service{Engine: eng, Journal: j}
	req := domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
		SlotKey:     "pilot-device|pilot-install|pilot|2026-09-09T00:00:00Z",
	}
	a, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != domain.PhaseSucceeded {
		t.Fatalf("first: %s %s", a.Outcome, a.ErrorMessage)
	}
	b, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if eng.called != 1 {
		t.Fatalf("engine called %d times", eng.called)
	}
	if b.ErrorMessage != "logical slot already completed" {
		t.Fatalf("%q", b.ErrorMessage)
	}
}

// An operator-triggered "Backup Now" has no logical slot of its own -- unlike
// the scheduler's once-per-day date slot or a qualification run's
// once-per-run slot, reusing a fixed slot across distinct operator clicks
// would replay the first click's cached result forever and never run restic
// again. Confirmed live: a synthetic device's second and third "Backup Now"
// clicks need to each produce their own attempt.
func TestEmptySlotKeyNeverReusesAPriorJob(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &okEngine{}
	s := &Service{Engine: eng, Journal: j}
	req := domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
		SlotKey:     "",
	}
	a, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != domain.PhaseSucceeded {
		t.Fatalf("first: %s %s", a.Outcome, a.ErrorMessage)
	}
	b, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if b.Outcome != domain.PhaseSucceeded {
		t.Fatalf("second: %s %s", b.Outcome, b.ErrorMessage)
	}
	if b.ErrorMessage == "logical slot already completed" {
		t.Fatal("an operator backup with no slot must never be served a cached result")
	}
	if eng.called != 2 {
		t.Fatalf("both operator-triggered backups must actually run restic, engine called %d times", eng.called)
	}
	if a.JobID == b.JobID {
		t.Fatalf("each unslotted backup must get its own job, got the same JobID %q twice", a.JobID)
	}
}

type cancelThenOK struct {
	okEngine
	cancel context.CancelFunc
}

func (e *cancelThenOK) Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
	if e.cancel != nil {
		e.cancel()
	}
	return e.okEngine.Backup(ctx, req)
}

func TestJournalCommitsAfterCallerContextCancelled(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx, cancel := context.WithCancel(context.Background())
	eng := &cancelThenOK{cancel: cancel}
	s := &Service{Engine: eng, Journal: j}
	res, err := s.Backup(ctx, domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.PhasePartial || !res.PublishedNotGreen {
		t.Fatalf("journal must still commit after cancel: %s %s", res.Outcome, res.ErrorMessage)
	}
	if !res.CleanupOK {
		t.Fatal("cleanup/journal should succeed with durable context")
	}
}

func TestQualificationTerminalDoesNotRelaunchOrUseScheduledSlot(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &okEngine{}
	s := &Service{Engine: eng, Journal: j}
	src := t.TempDir()
	today := "pilot-device|pilot-install|pilot/pilot-local-1|2026-09-09T09:00:00Z"
	if _, err := s.Backup(context.Background(), domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{src},
		VSSMode:     domain.VSSDisabled,
		SlotKey:     today,
	}); err != nil {
		t.Fatal(err)
	}
	runID := "qualrun-app1"
	req := domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots:        []string{src},
		VSSMode:            domain.VSSDisabled,
		AttemptClass:       domain.AttemptQualification,
		QualificationRunID: runID,
		JobID:              domain.QualificationJobID(runID),
		AttemptID:          domain.QualificationAttemptID(runID),
		SlotKey:            domain.QualificationSlotKey(runID),
	}
	a, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != domain.PhaseSucceeded || a.AttemptClass != domain.AttemptQualification {
		t.Fatalf("%+v", a)
	}
	called := eng.called
	b, err := s.Backup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if eng.called != called {
		t.Fatal("qualification replay launched engine")
	}
	if b.ErrorMessage != "qualification run already completed" {
		t.Fatalf("%q", b.ErrorMessage)
	}
	slot, err := j.LatestSucceededBackupSlot(context.Background())
	if err != nil || slot != today {
		t.Fatalf("slot=%s err=%v", slot, err)
	}
}

func TestFinishQueuesACatalogBuildOutboxEventOnlyForASuccessfulBackupWithASnapshot(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &okEngine{}
	s := &Service{Engine: eng, Journal: j}
	res, err := s.Backup(context.Background(), domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := j.UnackedOutbox(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	var sawTerminal, sawCatalog bool
	for _, ev := range rows {
		switch ev.Type {
		case "backup.terminal":
			sawTerminal = true
		case "catalog.build":
			sawCatalog = true
			var payload struct {
				SnapshotID string `json:"snapshot_id"`
			}
			if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SnapshotID != string(res.SnapshotID) {
				t.Fatalf("catalog.build snapshot id = %s, want %s", payload.SnapshotID, res.SnapshotID)
			}
		}
	}
	if !sawTerminal || !sawCatalog {
		t.Fatalf("expected both backup.terminal and catalog.build outbox events, got %+v", rows)
	}
}

func TestFinishNeverQueuesACatalogBuildEventWhenThereIsNoSnapshot(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &boomEngine{} // returns PhaseSucceeded with an empty SnapshotID
	s := &Service{Engine: eng, Journal: j}
	if _, err := s.Backup(context.Background(), domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{t.TempDir()},
		VSSMode:     domain.VSSDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := j.UnackedOutbox(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range rows {
		if ev.Type == "catalog.build" {
			t.Fatalf("must never queue a catalog.build event for a result with no snapshot id: %+v", rows)
		}
	}
}
