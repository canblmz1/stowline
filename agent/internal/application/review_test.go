package application

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

type terminalFailureJournal struct {
	ports.Journal
	bounded bool
}

func (j *terminalFailureJournal) CompleteAttempt(ctx context.Context, _ ports.AttemptRecord, _ domain.JobState) error {
	deadline, ok := ctx.Deadline()
	j.bounded = ok && time.Until(deadline) <= 10*time.Second && ctx.Err() == nil
	return errors.New("injected terminal commit failure")
}

type restoreEngine struct {
	okEngine
	result domain.RestoreResult
}

func (e *restoreEngine) Restore(context.Context, domain.RestoreRequest) (domain.RestoreResult, error) {
	return e.result, nil
}

func TestReviewRestoreRequiresVerificationAndTerminalCommit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     domain.RestoreResult
		failCommit bool
	}{
		{name: "empty engine result"},
		{name: "unverified result", result: domain.RestoreResult{State: domain.RestoreVerifying}},
		{name: "failed journal", result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}, failCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, err := journal.Open(filepath.Join(t.TempDir(), "journal.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			var store ports.Journal = j
			failure := &terminalFailureJournal{Journal: j}
			if tc.failCommit {
				store = failure
			}
			s := Service{Engine: &restoreEngine{result: tc.result}, Journal: store}
			result, err := s.Restore(context.Background(), domain.RestoreRequest{Repository: storage.LocalDescriptor(t.TempDir(), "g", "d"), SnapshotID: domain.SnapshotID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), StagingRoot: t.TempDir()})
			if result.State == domain.RestoreReady {
				t.Fatal("restore falsely READY")
			}
			if tc.failCommit && (err == nil || !failure.bounded) {
				t.Fatal("terminal failure must propagate and use bounded independent context")
			}
		})
	}
}

func TestReviewBackupTerminalFailurePropagates(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	failure := &terminalFailureJournal{Journal: j}
	s := Service{Engine: &okEngine{}, Journal: failure}
	result, err := s.Backup(context.Background(), domain.BackupRequest{Repository: storage.LocalDescriptor(t.TempDir(), "g", "d"), SourceRoots: []string{t.TempDir()}, VSSMode: domain.VSSDisabled})
	if err == nil || result.Outcome == domain.PhaseSucceeded || !failure.bounded {
		t.Fatal("terminal commit failure was swallowed or unbounded")
	}
}
