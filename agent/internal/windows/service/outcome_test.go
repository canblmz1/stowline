package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

type recLog struct {
	info, warn, errn []string
}

func (r *recLog) Info(_ uint32, msg string) error {
	r.info = append(r.info, msg)
	return nil
}
func (r *recLog) Warning(_ uint32, msg string) error {
	r.warn = append(r.warn, msg)
	return nil
}
func (r *recLog) Error(_ uint32, msg string) error {
	r.errn = append(r.errn, msg)
	return nil
}

func TestWrapBackupFailedProducesTickError(t *testing.T) {
	err := WrapBackup(domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ErrorClass: domain.ErrorAuth,
		AttemptID:  "att-1",
	}, nil)
	var te *TickError
	if !errors.As(err, &te) {
		t.Fatalf("got %v", err)
	}
	if te.Class != domain.ErrorAuth || te.Outcome != domain.PhaseFailed {
		t.Fatalf("%+v", te)
	}
	if err.Error() != "Scheduled backup FAILED: class=AUTH attempt=att-1" {
		t.Fatalf("%q", err.Error())
	}
}

func TestReportTickFailedAuthIsError(t *testing.T) {
	log := &recLog{}
	var last atomic.Pointer[string]
	ReportTick(log, context.Background(), WrapBackup(domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ErrorClass: domain.ErrorAuth,
		AttemptID:  "att-auth",
	}, nil), &last)
	if len(log.errn) != 1 || log.errn[0] != "Scheduled backup FAILED: class=AUTH attempt=att-auth" {
		t.Fatalf("errors=%v info=%v warn=%v", log.errn, log.info, log.warn)
	}
	ReportTick(log, context.Background(), WrapBackup(domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ErrorClass: domain.ErrorAuth,
		AttemptID:  "att-auth",
	}, nil), &last)
	if len(log.errn) != 1 {
		t.Fatalf("identical AUTH failures must be deduped, got %v", log.errn)
	}
}

func TestReportTickPartialIsWarning(t *testing.T) {
	log := &recLog{}
	ReportTick(log, context.Background(), WrapBackup(domain.BackupResult{
		Outcome:    domain.PhasePartial,
		ErrorClass: domain.ErrorSourceUnreadable,
		AttemptID:  "att-p",
	}, nil), nil)
	if len(log.errn) != 0 {
		t.Fatalf("PARTIAL must not be Error: %v", log.errn)
	}
	if len(log.warn) != 1 || log.warn[0] != "Scheduled backup PARTIAL: class=SOURCE_UNREADABLE attempt=att-p" {
		t.Fatalf("warn=%v", log.warn)
	}
}

func TestReportTickSCMCancelNotError(t *testing.T) {
	log := &recLog{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ReportTick(log, ctx, WrapBackup(domain.BackupResult{
		Outcome:    domain.PhaseCancelled,
		ErrorClass: domain.ErrorCancelled,
		AttemptID:  "att-c",
	}, nil), nil)
	if len(log.errn) != 0 {
		t.Fatalf("SCM cancel must not emit Error: %v", log.errn)
	}
	if len(log.info) != 1 {
		t.Fatalf("info=%v", log.info)
	}
	ReportTick(log, ctx, context.Canceled, nil)
	if len(log.errn) != 0 {
		t.Fatalf("context.Canceled must not emit Error: %v", log.errn)
	}
	ReportTick(log, ctx, WrapBackup(domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ErrorClass: domain.ErrorAuth,
		AttemptID:  "att-auth",
	}, nil), nil)
	if len(log.errn) != 1 {
		t.Fatalf("terminal AUTH must still emit Error even if stop raced: %v", log.errn)
	}
	if log.errn[0] != "Scheduled backup FAILED: class=AUTH attempt=att-auth" {
		t.Fatalf("%q", log.errn[0])
	}
}

func TestWrapBackupDoesNotLeakSecret(t *testing.T) {
	err := WrapBackup(domain.BackupResult{
		Outcome:      domain.PhaseFailed,
		ErrorClass:   domain.ErrorAuth,
		ErrorMessage: domain.CanarySecret,
		AttemptID:    "att-s",
	}, fmt.Errorf("restic: %s", domain.CanarySecret))
	if err == nil {
		t.Fatal("expected error")
	}
	if domain.ContainsSecret(err.Error()) {
		t.Fatalf("secret leaked: %q", err.Error())
	}
}

func TestWrapBackupQualificationIsNotScheduledEvent(t *testing.T) {
	err := WrapBackup(domain.BackupResult{
		Outcome:      domain.PhaseSucceeded,
		AttemptID:    "qual-att-1",
		AttemptClass: domain.AttemptQualification,
	}, nil)
	if err == nil || err.Error() != "Qualification backup SUCCEEDED attempt=qual-att-1" {
		t.Fatalf("%v", err)
	}
	log := &recLog{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ReportTick(log, ctx, WrapBackup(domain.BackupResult{
		Outcome:      domain.PhaseCancelled,
		ErrorClass:   domain.ErrorCancelled,
		AttemptID:    "qual-att-c",
		AttemptClass: domain.AttemptQualification,
	}, nil), nil)
	if len(log.errn) != 0 {
		t.Fatalf("qual cancel must not be Error: %v", log.errn)
	}
	if len(log.info) != 1 || log.info[0] != "Qualification backup CANCELLED during service stop attempt=qual-att-c" {
		t.Fatalf("info=%v", log.info)
	}
}

func TestReportTickSuccessIsInfo(t *testing.T) {
	log := &recLog{}
	var last atomic.Pointer[string]
	msg := "prior"
	last.Store(&msg)
	ReportTick(log, context.Background(), WrapBackup(domain.BackupResult{
		Outcome:   domain.PhaseSucceeded,
		AttemptID: "att-ok",
	}, nil), &last)
	if len(log.errn) != 0 || len(log.warn) != 0 {
		t.Fatalf("success: %+v", log)
	}
	if len(log.info) != 1 {
		t.Fatalf("info=%v", log.info)
	}
	if last.Load() != nil {
		t.Fatal("success must clear dedupe so a later AUTH is logged")
	}
}

// Reproduces the live finding: an operator-triggered RUN_BACKUP command that
// genuinely succeeded (real snapshot, CONSISTENCY_VERIFIED) was still acked
// as error_class=INTERNAL, because the command dispatcher treats any non-nil
// error from the Backup hook as failure, and WrapBackup's non-nil-on-success
// TickError -- which only exists so ReportTick's Outcome switch can log a
// scheduler tick's success -- looked exactly like one.
func TestWrapOperatorBackupSuccessIsNilError(t *testing.T) {
	err := WrapOperatorBackup(domain.BackupResult{
		Outcome:    domain.PhaseSucceeded,
		AttemptID:  "att-op-1",
		SnapshotID: "e37050a51bd39c13f22a06097c5afe8c1993876531f1208da6be471006a32547",
	}, nil)
	if err != nil {
		t.Fatalf("a succeeded operator backup must ack as nil, got %v", err)
	}
}

func TestWrapOperatorBackupFailureStillClassifies(t *testing.T) {
	err := WrapOperatorBackup(domain.BackupResult{
		Outcome:    domain.PhaseFailed,
		ErrorClass: domain.ErrorAuth,
		AttemptID:  "att-op-2",
	}, nil)
	var te *TickError
	if !errors.As(err, &te) {
		t.Fatalf("a failed operator backup must still classify via TickError, got %v", err)
	}
	if te.Class != domain.ErrorAuth {
		t.Fatalf("%+v", te)
	}
}

func TestReportTickLogsEveryJoinedFailure(t *testing.T) {
	log := &recLog{}
	ReportTick(log, context.Background(), errors.Join(
		errors.New("event=ACK_FAILED stage=ack command_id=cmd-1 kind=BROWSE_LOCAL_DIR error_class=NETWORK"),
		WrapBackup(domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorAuth, AttemptID: "att-joined"}, nil),
	), nil)
	if len(log.errn) != 2 {
		t.Fatalf("both poll and scheduler failures must be visible: %v", log.errn)
	}
	if log.errn[0] != "backup tick failed: event=ACK_FAILED stage=ack command_id=cmd-1 kind=BROWSE_LOCAL_DIR error_class=NETWORK" {
		t.Fatalf("poll error missing or changed: %v", log.errn)
	}
	if log.errn[1] != "Scheduled backup FAILED: class=AUTH attempt=att-joined" {
		t.Fatalf("backup error missing or changed: %v", log.errn)
	}
}
