package journal

import (
	"context"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"path/filepath"
	"testing"
)

func TestReviewPartialIsNotFreshnessSuccess(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	att := ports.AttemptRecord{ID: "a", JobID: "j", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhasePreparing}
	if err := j.BeginJob(ctx, ports.JobRecord{ID: "j", Kind: domain.JobBackup, SlotKey: "today", State: domain.JobActive}, att); err != nil {
		t.Fatal(err)
	}
	att.Phase = domain.PhasePartial
	att.Outcome = domain.PhasePartial
	if err := j.CompleteAttempt(ctx, att, domain.JobPartial); err != nil {
		t.Fatal(err)
	}
	slot, err := j.LatestSucceededBackupSlot(ctx)
	if err != nil || slot != "" {
		t.Fatal("partial counted as last successful slot")
	}
}

func TestQualificationSuccessIsNotFreshness(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	sched := ports.AttemptRecord{ID: "s", JobID: "sj", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhasePreparing, SlotKey: "today-slot"}
	if err := j.BeginJob(ctx, ports.JobRecord{ID: "sj", Kind: domain.JobBackup, SlotKey: "today-slot", State: domain.JobActive}, sched); err != nil {
		t.Fatal(err)
	}
	sched.Phase = domain.PhaseSucceeded
	sched.Outcome = domain.PhaseSucceeded
	if err := j.CompleteAttempt(ctx, sched, domain.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	q := ports.AttemptRecord{ID: "q", JobID: "qj", AttemptNo: 1, Kind: domain.JobQualificationBackup, Phase: domain.PhasePreparing, SlotKey: "qualification|qualrun-001"}
	if err := j.BeginJob(ctx, ports.JobRecord{ID: "qj", Kind: domain.JobQualificationBackup, SlotKey: "qualification|qualrun-001", State: domain.JobActive}, q); err != nil {
		t.Fatal(err)
	}
	q.Phase = domain.PhaseSucceeded
	q.Outcome = domain.PhaseSucceeded
	if err := j.CompleteAttempt(ctx, q, domain.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	slot, err := j.LatestSucceededBackupSlot(ctx)
	if err != nil || slot != "today-slot" {
		t.Fatalf("qualification counted as freshness: %s %v", slot, err)
	}
}
func TestReviewNullableJournalRows(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if err := j.BeginJob(ctx, ports.JobRecord{ID: "j", Kind: domain.JobBackup, State: domain.JobActive}, ports.AttemptRecord{ID: "a", JobID: "j", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhasePreparing}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.GetJob(ctx, "j"); err != nil {
		t.Errorf("manual job: %v", err)
	}
	if err := j.AppendOutbox(ctx, ports.OutboxEvent{EventID: "e", Type: "test", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	rows, err := j.UnackedOutbox(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Errorf("unacked outbox: %v", err)
	}
}
