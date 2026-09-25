package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/application"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/scheduler"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

func testRepo() domain.RepositoryDescriptor {
	return domain.RepositoryDescriptor{
		BackendKind:  domain.BackendLocal,
		Location:     `C:\Stowline\Repos\local`,
		Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
	}
}

func testBackupReq(t *testing.T, src, slot string) domain.BackupRequest {
	t.Helper()
	return domain.BackupRequest{
		Repository:  testRepo(),
		SourceRoots: []string{src},
		VSSMode:     domain.VSSDisabled,
		SlotKey:     slot,
	}
}

type captureEngine struct {
	okEngine
	last domain.BackupRequest
}

func (e *captureEngine) Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
	e.last = req
	return e.okEngine.Backup(ctx, req)
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

func TestUnavailableByDefault(t *testing.T) {
	r := &Runner{Dir: t.TempDir()}
	if r.ModeEnabled() {
		t.Fatal("qualification mode must be off by default")
	}
	if err := r.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRequiresQualificationModeAndIdentity(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	eng := &okEngine{}
	r := &Runner{
		Dir:           dir,
		AllowedSource: src,
		ConfigSources: []string{src},
		Repository:    testRepo(),
		VSSMode:       domain.VSSDisabled,
		AllowExecute:  func() bool { return false },
		Backup: func(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
			eng.called++
			return domain.BackupResult{}, nil
		},
	}
	if err := WriteEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteRequestFile(dir, validReq(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background()); err == nil {
		t.Fatal("must require LocalSystem")
	}
	if eng.called != 0 {
		t.Fatal("engine must not run")
	}
}

func TestOneRequestExecutesOnceAndReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	eng := &okEngine{}
	svc := &application.Service{Engine: eng, Journal: j}
	r := &Runner{
		Dir:           dir,
		AllowedSource: src,
		ConfigSources: []string{src},
		Repository:    testRepo(),
		VSSMode:       domain.VSSDisabled,
		Journal:       j,
		AllowExecute:  func() bool { return true },
		WorkloadBytes: 1024,
		Backup:        svc.Backup,
	}
	if err := WriteEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	req := validReq()
	if err := WriteRequestFile(dir, req, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background()); err != nil {
		requireTick(t, err, domain.PhaseSucceeded)
	}
	if eng.called != 1 {
		t.Fatalf("called %d", eng.called)
	}
	if err := r.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.called != 1 {
		t.Fatalf("replay launched engine: %d", eng.called)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ResultName))
	if err != nil {
		t.Fatal(err)
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.PhaseSucceeded || !res.Replay {
		t.Fatalf("%+v", res)
	}
}

func TestQualificationDoesNotAdvanceScheduledFreshnessOrTomorrowSlot(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	src := t.TempDir()
	today := "pilot-device|pilot-install|pilot/pilot-local-1|2026-09-09T09:00:00Z"
	eng := &okEngine{}
	svc := &application.Service{Engine: eng, Journal: j}
	sched := testBackupReq(t, src, today)
	if _, err := svc.Backup(ctx, sched); err != nil {
		t.Fatal(err)
	}
	slot, err := j.LatestSucceededBackupSlot(ctx)
	if err != nil || slot != today {
		t.Fatalf("scheduled slot %s err=%v", slot, err)
	}
	qreq := BoundBackupRequest("qualrun-xyz", []string{src}, testRepo(), domain.VSSDisabled, domain.SecretRef{}, "pilot-device", "pilot-install", "pilot-local-1", src, "pilot-device", 0, 0)
	res, err := svc.Backup(ctx, qreq)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.PhaseSucceeded || res.AttemptClass != domain.AttemptQualification {
		t.Fatalf("%+v", res)
	}
	slot, err = j.LatestSucceededBackupSlot(ctx)
	if err != nil || slot != today {
		t.Fatalf("qualification advanced freshness to %s", slot)
	}
	job, err := j.JobBySlot(ctx, today)
	if err != nil || job == nil || job.State != domain.JobSucceeded {
		t.Fatal("scheduled job mutated")
	}
	pol := domain.DefaultPilotPolicy([]string{src})
	epoch := scheduler.ScheduleEpoch(pol.Schedule.Epoch, pol.RevisionID)
	_, todayKey, err := scheduler.NextSlot("pilot-device", "pilot-install", epoch, pol.Schedule, mustIstanbul(t, 2026, 9, 9, 15, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, tomorrow, err := scheduler.NextSlot("pilot-device", "pilot-install", epoch, pol.Schedule, mustIstanbul(t, 2026, 9, 10, 15, 0))
	if err != nil {
		t.Fatal(err)
	}
	if todayKey == tomorrow {
		t.Fatal("tomorrow must form a distinct slot")
	}
	d := scheduler.Decide(scheduler.Input{
		Now:              mustIstanbul(t, 2026, 9, 9, 15, 0),
		Due:              mustIstanbul(t, 2026, 9, 9, 12, 0),
		TodayKey:         todayKey,
		LastSucceededKey: today,
		TodayState:       domain.JobSucceeded,
	})
	if d.Run || d.Reason != scheduler.ReasonSkipComplete {
		t.Fatalf("scheduler changed: %+v", d)
	}
	d2 := scheduler.Decide(scheduler.Input{
		Now:              mustIstanbul(t, 2026, 9, 10, 15, 0),
		Due:              mustIstanbul(t, 2026, 9, 10, 12, 0),
		TodayKey:         tomorrow,
		LastSucceededKey: today,
		CatchUp:          true,
		Location:         mustIstanbulLoc(t),
	})
	if !d2.Run {
		t.Fatalf("tomorrow must still be schedulable: %+v", d2)
	}
}

func TestFailedAndCancelledQualificationNeverBecomeScheduledSuccess(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	src := t.TempDir()
	today := "pilot-device|pilot-install|pilot/pilot-local-1|2026-09-09T09:00:00Z"
	svc := &application.Service{Engine: &okEngine{}, Journal: j}
	if _, err := svc.Backup(ctx, testBackupReq(t, src, today)); err != nil {
		t.Fatal(err)
	}
	failEng := &failOnceEngine{errClass: domain.ErrorAuth}
	failSvc := &application.Service{Engine: failEng, Journal: j}
	q := BoundBackupRequest("qualrun-fail", []string{src}, testRepo(), domain.VSSDisabled, domain.SecretRef{}, "d", "i", "r", src, "d", 0, 0)
	res, _ := failSvc.Backup(ctx, q)
	if res.Outcome == domain.PhaseSucceeded {
		t.Fatal("failed qualification became success")
	}
	slot, _ := j.LatestSucceededBackupSlot(ctx)
	if slot != today {
		t.Fatalf("failed qualification overwrote slot %s", slot)
	}
	q2 := BoundBackupRequest("qualrun-fail", []string{src}, testRepo(), domain.VSSDisabled, domain.SecretRef{}, "d", "i", "r", src, "d", 0, 0)
	res2, _ := failSvc.Backup(ctx, q2)
	if failEng.called != 1 {
		t.Fatalf("failed qualification relaunched: %d", failEng.called)
	}
	if res2.ErrorMessage != "qualification run already completed" {
		t.Fatalf("%q", res2.ErrorMessage)
	}
}

func TestRunnerBindsConfiguredRepoAndSourceOnly(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	eng := &captureEngine{}
	svc := &application.Service{Engine: eng, Journal: mustJournal(t)}
	repo := testRepo()
	r := &Runner{
		Dir:           dir,
		AllowedSource: src,
		ConfigSources: []string{src},
		Repository:    repo,
		VSSMode:       domain.VSSDisabled,
		Journal:       svc.Journal,
		AllowExecute:  func() bool { return true },
		WorkloadBytes: 512,
		Backup:        svc.Backup,
	}
	if err := WriteEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteRequestFile(dir, validReq(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background()); err != nil {
		requireTick(t, err, domain.PhaseSucceeded)
	}
	if eng.last.Repository.Location != repo.Location {
		t.Fatalf("repo=%s", eng.last.Repository.Location)
	}
	if len(eng.last.SourceRoots) != 1 || !SamePath(eng.last.SourceRoots[0], src) {
		t.Fatalf("source=%v", eng.last.SourceRoots)
	}
	if eng.last.VSSMode != domain.VSSDisabled {
		t.Fatal(eng.last.VSSMode)
	}
	if len(eng.last.Excludes) != 0 {
		t.Fatal(eng.last.Excludes)
	}
}

func TestControllerTickUnchangedAfterQualification(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	src := t.TempDir()
	svc := &application.Service{Engine: &okEngine{}, Journal: j}
	today := "pilot-device|pilot-install|pilot/pilot-local-1|2026-09-09T09:00:00Z"
	if _, err := svc.Backup(ctx, testBackupReq(t, src, today)); err != nil {
		t.Fatal(err)
	}
	q := BoundBackupRequest("qualrun-ctl", []string{src}, testRepo(), domain.VSSDisabled, domain.SecretRef{}, "pilot-device", "pilot-install", "pilot-local-1", src, "pilot-device", 0, 0)
	if _, err := svc.Backup(ctx, q); err != nil {
		t.Fatal(err)
	}
	launches := 0
	ctrl := &scheduler.Controller{
		DeviceID:         "pilot-device",
		InstallationID:   "pilot-install",
		Policy:           domain.DefaultPilotPolicy([]string{src}).Schedule,
		PolicyRevisionID: "pilot-local-1",
		Journal:          j,
		Clock:            ports.FrozenClock{T: mustIstanbul(t, 2026, 9, 9, 15, 0)},
		Backup: func(ctx context.Context, slotKey string) error {
			launches++
			return nil
		},
	}
	d, err := ctrl.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run || launches != 0 || d.Reason != scheduler.ReasonSkipComplete {
		t.Fatalf("tick=%+v launches=%d", d, launches)
	}
}

type failOnceEngine struct {
	called   int
	errClass domain.ErrorClass
}

func (e *failOnceEngine) Version(context.Context) (string, error) { return "restic 0.19.1", nil }
func (e *failOnceEngine) Init(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *failOnceEngine) Backup(context.Context, domain.BackupRequest) (domain.BackupResult, error) {
	e.called++
	return domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ExitKnown:  true,
		ErrorClass: e.errClass,
	}, nil
}
func (e *failOnceEngine) Snapshots(context.Context, domain.RepositoryDescriptor, domain.SecretRef) ([]ports.Snapshot, error) {
	return nil, nil
}
func (e *failOnceEngine) Check(context.Context, domain.RepositoryDescriptor, domain.SecretRef, bool) (ports.CheckResult, error) {
	return ports.CheckResult{}, nil
}
func (e *failOnceEngine) Restore(context.Context, domain.RestoreRequest) (domain.RestoreResult, error) {
	return domain.RestoreResult{}, nil
}
func (e *failOnceEngine) CatConfig(context.Context, domain.RepositoryDescriptor, domain.SecretRef) (string, error) {
	return "", nil
}
func (e *failOnceEngine) ReadSnapshot(context.Context, domain.RepositoryDescriptor, domain.SecretRef, domain.SnapshotID) (*ports.Snapshot, error) {
	return nil, nil
}

func requireTick(t *testing.T, err error, want domain.AttemptPhase) {
	t.Helper()
	var te *winsvc.TickError
	if !errors.As(err, &te) {
		t.Fatalf("got %v", err)
	}
	if te.Outcome != want {
		t.Fatalf("outcome %s want %s (%s)", te.Outcome, want, err)
	}
	if te.AttemptClass != domain.AttemptQualification {
		t.Fatalf("attempt class %s", te.AttemptClass)
	}
}

func mustJournal(t *testing.T) *journal.SQLite {
	t.Helper()
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func mustIstanbulLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustIstanbul(t *testing.T, year, month, day, hour, min int) time.Time {
	t.Helper()
	return time.Date(year, time.Month(month), day, hour, min, 0, 0, mustIstanbulLoc(t))
}
