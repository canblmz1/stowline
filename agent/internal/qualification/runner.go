package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

const (
	EnabledName = "enabled"
	RequestName = "request.json"
	ResultName  = "result.json"
)

// Runner consumes one local qualification request inside the SCM service process.
// It is disabled unless the enabled marker exists. It cannot choose a repository,
// source, or argv; those come from the bound service configuration.
type Runner struct {
	Dir              string
	AllowedSource    string
	ConfigSources    []string
	Repository       domain.RepositoryDescriptor
	VSSMode          domain.VSSMode
	PasswordRef      domain.SecretRef
	DeviceID         string
	InstallationID   string
	PolicyRevisionID string
	CacheDir         string
	Host             string
	FreeSpaceFloor   int64
	BandwidthKiBps   int
	WorkloadBytes    int64
	AllowExecute     func() bool
	InspectACL       func(path string) error
	Journal          ports.Journal
	Backup           func(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error)
}

func (r *Runner) ModeEnabled() bool {
	if r == nil || r.Dir == "" {
		return false
	}
	p := filepath.Join(r.Dir, EnabledName)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if r.InspectACL != nil {
		if err := r.InspectACL(p); err != nil {
			return false
		}
	}
	return true
}

func (r *Runner) Poll(ctx context.Context) error {
	if r == nil || !r.ModeEnabled() {
		return nil
	}
	if r.AllowExecute != nil && !r.AllowExecute() {
		return fmt.Errorf("%w: qualification backup requires LocalSystem", domain.ErrConfig)
	}
	reqPath := filepath.Join(r.Dir, RequestName)
	raw, err := os.ReadFile(reqPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if r.InspectACL != nil {
		if err := r.InspectACL(reqPath); err != nil {
			return r.failResult("", domain.ErrorConfig, err.Error(), err)
		}
	}
	req, err := ParseRequest(raw)
	if err != nil {
		return r.failResult("", domain.ErrorConfig, err.Error(), err)
	}
	if r.Backup == nil {
		return r.failResult(req.QualificationRunID, domain.ErrorInternal, "qualification backup executor missing", domain.ErrConfig)
	}
	src, err := BindSources(r.ConfigSources, []string{r.AllowedSource})
	if err != nil {
		return r.failResult(req.QualificationRunID, domain.ErrorConfig, err.Error(), err)
	}
	if r.Journal != nil {
		existing, err := r.Journal.GetJob(ctx, domain.QualificationJobID(req.QualificationRunID))
		if err != nil {
			return err
		}
		if existing != nil && existing.State.Terminal() {
			prev, _ := r.Journal.LatestAttempt(ctx, existing.ID)
			res := replayResult(req.QualificationRunID, existing, prev)
			_ = r.writeResult(ResultFromBackup(req.QualificationRunID, res, true))
			return nil
		}
		if existing != nil && (existing.State == domain.JobActive || existing.State == domain.JobReconciling) {
			return nil
		}
	}
	if req.SyntheticCorpus {
		n := r.WorkloadBytes
		if n <= 0 {
			n = DefaultWorkloadBytes
		}
		if _, err := ExpandWorkload(src[0], req.QualificationRunID, n); err != nil {
			return r.failResult(req.QualificationRunID, domain.ErrorConfig, err.Error(), err)
		}
	}
	bReq := BoundBackupRequest(req.QualificationRunID, src, r.Repository, r.VSSMode, r.PasswordRef, r.DeviceID, r.InstallationID, r.PolicyRevisionID, r.CacheDir, r.Host, r.FreeSpaceFloor, r.BandwidthKiBps)
	res, runErr := r.Backup(ctx, bReq)
	if res.ErrorMessage == "qualification run already completed" {
		_ = r.writeResult(ResultFromBackup(req.QualificationRunID, res, true))
		return nil
	}
	if errors.Is(runErr, domain.ErrOperationInProgress) {
		return nil
	}
	_ = r.writeResult(ResultFromBackup(req.QualificationRunID, res, false))
	return winsvc.WrapBackup(res, runErr)
}

func (r *Runner) failResult(runID string, class domain.ErrorClass, msg string, err error) error {
	res := Result{
		Schema:             ResultSchema,
		QualificationRunID: runID,
		AttemptClass:       domain.AttemptQualification,
		Outcome:            domain.PhaseFailed,
		ErrorClass:         class,
		ErrorMessage:       msg,
	}
	_ = r.writeResult(res)
	return err
}

func (r *Runner) writeResult(res Result) error {
	if r.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(r.Dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.Dir, ResultName)
	if err := os.WriteFile(path, b, 0600); err != nil {
		return err
	}
	if r.InspectACL != nil {
		_ = acl.Apply(path, acl.ServiceState)
	}
	return nil
}

func replayResult(runID string, job *ports.JobRecord, prev *ports.AttemptRecord) domain.BackupResult {
	res := domain.BackupResult{
		AttemptClass:       domain.AttemptQualification,
		QualificationRunID: runID,
		JobID:              job.ID,
		ErrorMessage:       "qualification run already completed",
		RequiredRootsOK:    true,
		CleanupOK:          true,
	}
	if prev != nil {
		res.AttemptID = prev.ID
		res.SnapshotID = prev.SnapshotID
		res.ErrorClass = prev.ErrorClass
		if prev.SummaryJSON != "" {
			var stored domain.BackupResult
			if json.Unmarshal([]byte(prev.SummaryJSON), &stored) == nil && stored.Outcome != "" {
				stored.AttemptClass = domain.AttemptQualification
				stored.QualificationRunID = runID
				if stored.ErrorMessage == "" {
					stored.ErrorMessage = "qualification run already completed"
				}
				return stored
			}
		}
		if prev.Outcome != "" {
			res.Outcome = prev.Outcome
			return res
		}
	}
	switch job.State {
	case domain.JobSucceeded:
		res.Outcome = domain.PhaseSucceeded
	case domain.JobCancelled:
		res.Outcome = domain.PhaseCancelled
		res.ErrorClass = domain.ErrorCancelled
	case domain.JobPartial:
		res.Outcome = domain.PhasePartial
	default:
		res.Outcome = domain.PhaseFailed
	}
	return res
}

// WriteEnabled creates the qualification-mode marker. Default is absent.
func WriteEnabled(dir string, applyACL bool) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if applyACL {
		if err := acl.Apply(dir, acl.ServiceState); err != nil {
			return err
		}
	}
	p := filepath.Join(dir, EnabledName)
	if err := os.WriteFile(p, []byte("qualification_mode=1\n"), 0600); err != nil {
		return err
	}
	if applyACL {
		return acl.Apply(p, acl.ServiceState)
	}
	return nil
}

func WriteRequestFile(dir string, req Request, applyACL bool) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, RequestName)
	if err := os.WriteFile(p, b, 0600); err != nil {
		return err
	}
	if applyACL {
		if err := acl.Apply(dir, acl.ServiceState); err != nil {
			return err
		}
		return acl.Apply(p, acl.ServiceState)
	}
	return nil
}
