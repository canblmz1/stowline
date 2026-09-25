package journal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestBeginJobThenComplete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sqlite")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	job := ports.JobRecord{ID: "job1", Kind: domain.JobBackup, State: domain.JobActive, SlotKey: "slot-a"}
	att := ports.AttemptRecord{ID: "att1", JobID: "job1", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhasePreparing}
	if err := j.BeginJob(ctx, job, att); err != nil {
		t.Fatal(err)
	}
	att.Phase = domain.PhaseSucceeded
	att.Outcome = domain.PhaseSucceeded
	att.SnapshotID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := j.CompleteAttempt(ctx, att, domain.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	got, err := j.GetAttempt(ctx, "att1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != domain.PhaseSucceeded {
		t.Fatalf("%+v", got)
	}
	inc, err := j.IncompleteAttempts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inc) != 0 {
		t.Fatalf("incomplete: %+v", inc)
	}
}

func TestSlotRetryUsesNewAttempt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sqlite")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	job := ports.JobRecord{ID: "job1", Kind: domain.JobBackup, State: domain.JobActive, SlotKey: "slot-day"}
	att := ports.AttemptRecord{ID: "att1", JobID: "job1", AttemptNo: 1, Kind: domain.JobBackup, Phase: domain.PhasePreparing, SlotKey: "slot-day"}
	if err := j.BeginJob(ctx, job, att); err != nil {
		t.Fatal(err)
	}
	att.Phase = domain.PhaseFailed
	att.Outcome = domain.PhaseFailed
	if err := j.CompleteAttempt(ctx, att, domain.JobFailed); err != nil {
		t.Fatal(err)
	}
	if err := j.SetJobState(ctx, "job1", domain.JobActive); err != nil {
		t.Fatal(err)
	}
	att2 := ports.AttemptRecord{ID: "att2", JobID: "job1", AttemptNo: 2, Kind: domain.JobBackup, Phase: domain.PhasePreparing, SlotKey: "slot-day"}
	if err := j.BeginAttempt(ctx, att2); err != nil {
		t.Fatal(err)
	}
	got, err := j.LatestAttempt(ctx, "job1")
	if err != nil || got == nil || got.ID != "att2" {
		t.Fatalf("%v %+v", err, got)
	}
	slot, err := j.JobBySlot(ctx, "slot-day")
	if err != nil || slot == nil {
		t.Fatal(err)
	}
}

func TestCommandIdempotency(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sqlite")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	already, err := j.ConsumeCommand(ctx, "cmd-1", "job", true)
	if err != nil || already {
		t.Fatalf("first: %v %v", already, err)
	}
	already, err = j.ConsumeCommand(ctx, "cmd-1", "job", true)
	if err != nil || !already {
		t.Fatalf("second should be already: %v %v", already, err)
	}
}

func TestCommandFinishDoesNotInventSuccess(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sqlite")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	already, err := j.ConsumeCommand(ctx, "cmd-crash", "job", false)
	if err != nil || already {
		t.Fatalf("consume: %v %v", already, err)
	}
	term, state, extra, err := j.CommandResult(ctx, "cmd-crash")
	if err != nil || term || state != "" || extra != nil {
		t.Fatalf("pre-finish must be non-terminal empty: %v %v %q %v", term, state, extra, err)
	}
	if err := j.FinishCommand(ctx, "cmd-crash", "FAILED", nil); err != nil {
		t.Fatal(err)
	}
	term, state, extra, err = j.CommandResult(ctx, "cmd-crash")
	if err != nil || !term || state != "FAILED" || extra != nil {
		t.Fatalf("finish: %v %q %v %v", term, state, extra, err)
	}
}

func TestCommandFinishStoresAndReplaysExtraResultJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sqlite")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if _, err := j.ConsumeCommand(ctx, "cmd-browse", "job", false); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"path": `C:\Users\Public\Documents`, "entries": []any{map[string]any{"name": "a.txt", "type": "file"}}}
	if err := j.FinishCommand(ctx, "cmd-browse", "SUCCEEDED", want); err != nil {
		t.Fatal(err)
	}
	term, state, extra, err := j.CommandResult(ctx, "cmd-browse")
	if err != nil || !term || state != "SUCCEEDED" {
		t.Fatalf("finish: %v %q %v", term, state, err)
	}
	if extra["path"] != want["path"] {
		t.Fatalf("extra path lost on replay: %v", extra)
	}
	entries, ok := extra["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("extra entries lost on replay: %v", extra)
	}
}
