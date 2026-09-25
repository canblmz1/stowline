package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func waitState(t *testing.T, r *restoreJobs, id, want string) restoreJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.Get(id); ok && j.State == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := r.Get(id)
	t.Fatalf("job %s state = %q, want %q", id, j.State, want)
	return j
}

func TestRestoreJobRunsInTheBackgroundAndReportsProgressThenDone(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	var granted string
	r := &restoreJobs{
		StagingRoot: root,
		Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			progress(domain.RestoreProgress{PercentDone: 40, BytesDone: 40, TotalBytes: 100, FilesDone: 1, TotalFiles: 3})
			<-release
			dir := filepath.Join(root, jobID)
			return dir, os.MkdirAll(dir, 0o700)
		},
		GrantRead: func(dir string) error { granted = dir; return nil },
	}
	job, err := r.Start("a", []string{"/C/x"})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != restoreRunning {
		t.Fatalf("Start must return at once with a running job, got %q", job.State)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if j, _ := r.Get(job.ID); j.PercentDone == 40 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("progress never reached the job")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cur, ok := r.Current(); !ok || cur.ID != job.ID {
		t.Fatal("Current must return the running restore")
	}
	close(release)
	done := waitState(t, r, job.ID, restoreDone)
	if done.PercentDone != 100 || done.BytesDone != 100 || done.Staging == "" || granted != done.Staging {
		t.Fatalf("done job = %+v granted=%q", done, granted)
	}
}

func TestOnlyOneRestoreRunsAtATime(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	r := &restoreJobs{Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
		<-release
		return "", nil
	}}
	if _, err := r.Start("a", []string{"/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start("a", []string{"/y"}); !errors.Is(err, errRestoreBusy) {
		t.Fatalf("second start err = %v", err)
	}
}

func TestCancellingARestoreStopsItAndRemovesItsStaging(t *testing.T) {
	root := t.TempDir()
	var dir string
	r := &restoreJobs{
		StagingRoot: root,
		Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			dir = filepath.Join(root, jobID)
			_ = os.MkdirAll(dir, 0o700)
			<-ctx.Done()
			return dir, ctx.Err()
		},
	}
	job, _ := r.Start("a", []string{"/x"})
	time.Sleep(20 * time.Millisecond)
	if !r.Cancel(job.ID) {
		t.Fatal("cancel of a running job must succeed")
	}
	waitState(t, r, job.ID, restoreCancelled)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("staging should be removed after cancel, stat err = %v", err)
	}
}

func TestAFailedRestoreKeepsItsError(t *testing.T) {
	r := &restoreJobs{Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
		return "", errors.New("restore admission SITE_CAPACITY_WAIT")
	}}
	job, _ := r.Start("a", []string{"/x"})
	failed := waitState(t, r, job.ID, restoreFailed)
	if failed.Error == "" {
		t.Fatal("error must be kept for the page")
	}
}

func TestDeliveredRemovesStagingAndReportsTheUsersFolderOnce(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var reported []string
	r := &restoreJobs{
		StagingRoot: root,
		Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			dir := filepath.Join(root, jobID)
			return dir, os.MkdirAll(dir, 0o700)
		},
		Report: func(ctx context.Context, snapshotID string, selections []string, destination string) error {
			mu.Lock()
			reported = append(reported, destination)
			mu.Unlock()
			return nil
		},
		ReportDelay: time.Hour,
	}
	job, _ := r.Start("a", []string{"/x"})
	done := waitState(t, r, job.ID, restoreDone)
	if err := r.Delivered(job.ID, `C:\Users\ayse\Documents\Geri Yüklenenler\23 Eylül 10.44`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(done.Staging); !os.IsNotExist(err) {
		t.Fatal("staging must be removed once delivered")
	}
	r.reportOnce(job.ID, "") // the delayed fallback firing later must not report again
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || reported[0] != `C:\Users\ayse\Documents\Geri Yüklenenler\23 Eylül 10.44` {
		t.Fatalf("reported = %v", reported)
	}
}

func TestStagingOutsideTheRootIsNeverDeleted(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	r := &restoreJobs{StagingRoot: root}
	r.removeStagingLocked(outside)
	r.removeStagingLocked(root)
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a directory outside the staging root was deleted")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the staging root itself was deleted")
	}
}

func TestJobsSurviveARestartAndARunningOneShowsAsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-restores.json")
	release := make(chan struct{})
	r := &restoreJobs{StatePath: path, Run: func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
		<-release
		return "", nil
	}}
	job, _ := r.Start("a", []string{"/x"})
	// The job writes its final state into the temp dir after release; wait
	// for that before returning, or Windows cannot remove the temp dir.
	defer func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if j, ok := r.Get(job.ID); ok && j.State != restoreRunning {
				return
			}
		}
	}()
	again := &restoreJobs{StatePath: path}
	again.load()
	got, ok := again.Get(job.ID)
	if !ok || got.State != restoreFailed || got.Error != "interrupted" {
		t.Fatalf("after restart = %+v ok=%v", got, ok)
	}
}
