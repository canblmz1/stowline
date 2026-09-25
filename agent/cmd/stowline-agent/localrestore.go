package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// Self-service restore job states, as the Stowline Backups page shows them.
const (
	restoreRunning   = "running"
	restoreDone      = "done"
	restoreFailed    = "failed"
	restoreCancelled = "cancelled"
)

// errRestoreBusy: only one self-service restore runs at a time.
var errRestoreBusy = errors.New("a restore is already running")

const maxRestoreJobs = 20

// restoreJob is one restore the person at the PC started. Counts only --
// file names stay in Selections, which never leave this machine except in
// the admin audit (the same data the panel's own restore shows).
type restoreJob struct {
	ID          string    `json:"id"`
	SnapshotID  string    `json:"snapshot_id"`
	Selections  []string  `json:"selections"`
	State       string    `json:"state"`
	PercentDone float64   `json:"percent_done"`
	FilesDone   int64     `json:"files_done"`
	TotalFiles  int64     `json:"total_files"`
	BytesDone   int64     `json:"bytes_done"`
	TotalBytes  int64     `json:"total_bytes"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	// Staging is where the agent restored to; the desktop app copies the
	// picked items from here into the user's own Documents and then reports
	// DeliveredTo, after which Staging is removed.
	Staging     string `json:"staging,omitempty"`
	DeliveredTo string `json:"delivered_to,omitempty"`
	Error       string `json:"error,omitempty"`
	Reported    bool   `json:"reported,omitempty"`
	// Owner is the SID of the Windows account that asked for it: only that
	// account sees the job and gets read access to what was restored.
	Owner string `json:"owner,omitempty"`

	cancel context.CancelFunc
}

// restoreJobs runs self-service restores in the background so a restore
// never depends on the page (or the app window) staying open, and keeps
// their progress and outcome where the page can poll them.
type restoreJobs struct {
	mu   sync.Mutex
	jobs []*restoreJob // most recent first

	// Run performs the restore into staging and returns the staging path.
	Run func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error)
	// GrantRead lets the job's owner read what was restored (the staging
	// root is SYSTEM/Administrators only).
	GrantRead func(dir, owner string) error
	// StagingRoot bounds what Delivered may delete.
	StagingRoot string
	// StatePath persists the job list across agent restarts; "" disables it.
	StatePath string
	// Report is the best-effort admin audit (Olaylar).
	Report func(ctx context.Context, snapshotID string, selections []string, destination string) error
	// ReportDelay: a restore nobody delivers is still reported with its
	// staging path after this long.
	ReportDelay time.Duration
	now         func() time.Time
}

func (r *restoreJobs) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// load restores the persisted list; anything still "running" was cut off by
// an agent restart and is shown as failed rather than silently vanishing.
func (r *restoreJobs) load() {
	if r.StatePath == "" {
		return
	}
	raw, err := os.ReadFile(r.StatePath)
	if err != nil {
		return
	}
	var jobs []*restoreJob
	if json.Unmarshal(raw, &jobs) != nil {
		return
	}
	for _, j := range jobs {
		if j.State == restoreRunning {
			j.State = restoreFailed
			j.Error = "interrupted"
			if j.EndedAt.IsZero() {
				j.EndedAt = r.clock()
			}
		}
	}
	r.mu.Lock()
	r.jobs = jobs
	r.mu.Unlock()
}

func (r *restoreJobs) saveLocked() {
	if r.StatePath == "" {
		return
	}
	raw, err := json.Marshal(r.jobs)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.StatePath), 0o700)
	tmp := r.StatePath + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, r.StatePath)
	}
}

func (r *restoreJobs) runningLocked() *restoreJob {
	for _, j := range r.jobs {
		if j.State == restoreRunning {
			return j
		}
	}
	return nil
}

// Start launches a restore in the background and returns immediately.
func (r *restoreJobs) Start(snapshotID string, selections []string) (restoreJob, error) {
	return r.StartAs("", snapshotID, selections)
}

// StartAs is Start for a restore that belongs to one Windows account.
func (r *restoreJobs) StartAs(owner, snapshotID string, selections []string) (restoreJob, error) {
	r.mu.Lock()
	if r.runningLocked() != nil {
		r.mu.Unlock()
		return restoreJob{}, errRestoreBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &restoreJob{
		ID:         string(domain.NewJobID()),
		SnapshotID: snapshotID,
		Selections: append([]string(nil), selections...),
		State:      restoreRunning,
		StartedAt:  r.clock(),
		Owner:      owner,
		cancel:     cancel,
	}
	r.jobs = append([]*restoreJob{job}, r.jobs...)
	if len(r.jobs) > maxRestoreJobs {
		r.jobs = r.jobs[:maxRestoreJobs]
	}
	r.saveLocked()
	snap := *job
	r.mu.Unlock()

	go r.run(ctx, job)
	return snap, nil
}

func (r *restoreJobs) run(ctx context.Context, job *restoreJob) {
	progress := func(p domain.RestoreProgress) {
		r.mu.Lock()
		job.PercentDone, job.FilesDone, job.TotalFiles, job.BytesDone, job.TotalBytes = p.PercentDone, p.FilesDone, p.TotalFiles, p.BytesDone, p.TotalBytes
		r.mu.Unlock()
	}
	staging, err := r.Run(ctx, job.ID, job.SnapshotID, job.Selections, progress)
	if err == nil && staging != "" && r.GrantRead != nil {
		if gerr := r.GrantRead(staging, job.Owner); gerr != nil {
			err = gerr
		}
	}
	r.mu.Lock()
	job.EndedAt = r.clock()
	job.cancel = nil
	switch {
	case ctx.Err() != nil:
		job.State = restoreCancelled
		r.removeStagingLocked(staging)
	case err != nil:
		job.State = restoreFailed
		job.Error = domain.Redact(err.Error())
		r.removeStagingLocked(staging)
	default:
		job.State = restoreDone
		job.Staging = staging
		job.PercentDone = 100
		if job.TotalBytes > 0 {
			job.BytesDone = job.TotalBytes
		}
		if job.TotalFiles > 0 {
			job.FilesDone = job.TotalFiles
		}
	}
	r.saveLocked()
	done := job.State == restoreDone
	id := job.ID
	r.mu.Unlock()

	if done && r.Report != nil && r.ReportDelay > 0 {
		time.AfterFunc(r.ReportDelay, func() { r.reportOnce(id, "") })
	}
}

// reportOnce sends the audit event for a job exactly once: with where the
// user ended up putting the files if they were delivered, otherwise with
// the staging path.
func (r *restoreJobs) reportOnce(id, destination string) {
	r.mu.Lock()
	var job *restoreJob
	for _, j := range r.jobs {
		if j.ID == id {
			job = j
		}
	}
	if job == nil || job.Reported || r.Report == nil {
		r.mu.Unlock()
		return
	}
	job.Reported = true
	if destination == "" {
		destination = job.Staging
	}
	snapshot, selections := job.SnapshotID, append([]string(nil), job.Selections...)
	r.saveLocked()
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.Report(ctx, snapshot, selections, destination)
	}()
}

// removeStagingLocked deletes a job's own staging directory, never anything
// outside StagingRoot.
func (r *restoreJobs) removeStagingLocked(staging string) {
	if staging == "" || r.StagingRoot == "" {
		return
	}
	root, err1 := filepath.Abs(r.StagingRoot)
	dir, err2 := filepath.Abs(staging)
	if err1 != nil || err2 != nil {
		return
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return
	}
	_ = os.RemoveAll(dir)
}

func (r *restoreJobs) Get(id string) (restoreJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.ID == id {
			return *j, true
		}
	}
	return restoreJob{}, false
}

func (r *restoreJobs) List() []restoreJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]restoreJob, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, *j)
	}
	return out
}

// ListFor is List limited to one account's own restores.
func (r *restoreJobs) ListFor(owner string) []restoreJob {
	out := []restoreJob{}
	for _, j := range r.List() {
		if j.Owner == owner {
			out = append(out, j)
		}
	}
	return out
}

// Current is the running restore, if any.
func (r *restoreJobs) Current() (restoreJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.runningLocked(); j != nil {
		return *j, true
	}
	return restoreJob{}, false
}

func (r *restoreJobs) Cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.ID == id && j.State == restoreRunning && j.cancel != nil {
			j.cancel()
			return true
		}
	}
	return false
}

// Delivered records that the desktop app copied the files into the user's
// own folder; the staging copy is then removed.
func (r *restoreJobs) Delivered(id, destination string) error {
	r.mu.Lock()
	var job *restoreJob
	for _, j := range r.jobs {
		if j.ID == id {
			job = j
		}
	}
	if job == nil {
		r.mu.Unlock()
		return errors.New("unknown restore")
	}
	if job.State != restoreDone {
		r.mu.Unlock()
		return errors.New("restore is not finished")
	}
	job.DeliveredTo = destination
	r.removeStagingLocked(job.Staging)
	job.Staging = ""
	r.saveLocked()
	r.mu.Unlock()
	r.reportOnce(id, destination)
	return nil
}
