package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/windows/restore"
)

// Service orchestrates backup/restore with durable intent and a single active operation.
type Service struct {
	Engine      ports.BackupEngine
	Journal     ports.Journal
	Clock       ports.Clock
	ManifestDir string
	mu          sync.Mutex
	active      bool
	opCancel    context.CancelFunc
}

func (s *Service) lockOp() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return domain.ErrOperationInProgress
	}
	s.active = true
	return nil
}

func (s *Service) unlockOp() {
	s.mu.Lock()
	s.active = false
	s.opCancel = nil
	s.mu.Unlock()
}

// CancelCurrent requests cancellation of the in-flight backup/restore. It does not delete snapshots.
func (s *Service) CancelCurrent() {
	s.mu.Lock()
	c := s.opCancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (s *Service) bindOpCancel(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.opCancel = cancel
	s.mu.Unlock()
	return ctx, cancel
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now().UTC()
}

func classify(req domain.BackupRequest, res domain.BackupResult) domain.BackupResult {
	res.AttemptClass = req.AttemptClass.Effective()
	res.QualificationRunID = req.QualificationRunID
	return res
}

func qualificationReplay(existing *ports.JobRecord, prev *ports.AttemptRecord) domain.BackupResult {
	res := domain.BackupResult{
		AttemptClass:    domain.AttemptQualification,
		JobID:           existing.ID,
		ErrorMessage:    "qualification run already completed",
		RequiredRootsOK: true,
		CleanupOK:       true,
	}
	if prev != nil {
		res.AttemptID = prev.ID
		res.SnapshotID = prev.SnapshotID
		res.ErrorClass = prev.ErrorClass
		if prev.SummaryJSON != "" {
			var stored domain.BackupResult
			if json.Unmarshal([]byte(prev.SummaryJSON), &stored) == nil && stored.Outcome != "" {
				stored.AttemptClass = domain.AttemptQualification
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
	switch existing.State {
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

func (s *Service) Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
	if err := req.Validate(); err != nil {
		return classify(req, domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorConfig, ErrorMessage: err.Error()}), err
	}
	if err := s.lockOp(); err != nil {
		return classify(req, domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}), err
	}
	defer s.unlockOp()
	ctx, cancelOp := s.bindOpCancel(ctx)
	defer cancelOp()

	if req.AttemptClass == domain.AttemptQualification {
		if req.JobID == "" {
			req.JobID = domain.QualificationJobID(req.QualificationRunID)
		}
		if req.AttemptID == "" {
			req.AttemptID = domain.QualificationAttemptID(req.QualificationRunID)
		}
		if req.SlotKey == "" {
			req.SlotKey = domain.QualificationSlotKey(req.QualificationRunID)
		}
	}
	if req.JobID == "" {
		req.JobID = domain.NewJobID()
	}
	if req.AttemptID == "" {
		req.AttemptID = domain.NewAttemptID()
	}

	attemptNo := 1
	reuseJob := false
	if req.SlotKey != "" {
		existing, err := s.Journal.JobBySlot(ctx, req.SlotKey)
		if err != nil {
			return classify(req, domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}), err
		}
		if existing != nil {
			if req.AttemptClass == domain.AttemptQualification {
				prev, _ := s.Journal.LatestAttempt(ctx, existing.ID)
				if existing.State.Terminal() {
					return classify(req, qualificationReplay(existing, prev)), nil
				}
				return classify(req, domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: domain.ErrOperationInProgress.Error()}), domain.ErrOperationInProgress
			}
			if existing.State == domain.JobSucceeded {
				prev, _ := s.Journal.LatestAttempt(ctx, existing.ID)
				res := domain.BackupResult{Outcome: domain.PhaseSucceeded, RequiredRootsOK: true, CleanupOK: true}
				if prev != nil {
					res.SnapshotID = prev.SnapshotID
					res.ErrorClass = prev.ErrorClass
				}
				res.ErrorMessage = "logical slot already completed"
				return classify(req, res), nil
			}
			if existing.State == domain.JobActive || existing.State == domain.JobReconciling {
				return classify(req, domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: domain.ErrOperationInProgress.Error()}), domain.ErrOperationInProgress
			}
			req.JobID = existing.ID
			reuseJob = true
			if prev, _ := s.Journal.LatestAttempt(ctx, existing.ID); prev != nil {
				attemptNo = prev.AttemptNo + 1
			}
		}
	}

	jobKind := req.AttemptClass.JobKind()
	job := ports.JobRecord{
		ID:               req.JobID,
		Kind:             jobKind,
		SlotKey:          req.SlotKey,
		PolicyRevisionID: req.PolicyRevisionID,
		State:            domain.JobActive,
		CreatedAt:        s.now(),
		UpdatedAt:        s.now(),
	}
	attempt := ports.AttemptRecord{
		ID:               req.AttemptID,
		JobID:            req.JobID,
		AttemptNo:        attemptNo,
		Kind:             jobKind,
		SlotKey:          req.SlotKey,
		PolicyRevisionID: req.PolicyRevisionID,
		Phase:            domain.PhasePreparing,
		IntentAt:         s.now(),
		StartedAt:        s.now(),
	}
	if reuseJob {
		if err := s.Journal.BeginAttempt(ctx, attempt); err != nil {
			return domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: domain.ErrIntentNotPersisted.Error()}, fmt.Errorf("%w", domain.ErrIntentNotPersisted)
		}
	} else if err := s.Journal.BeginJob(ctx, job, attempt); err != nil {
		return domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: domain.ErrIntentNotPersisted.Error()}, fmt.Errorf("%w", domain.ErrIntentNotPersisted)
	}

	if req.FreeSpaceFloor > 0 {
		if free, err := volumeFreeBytes(req.Repository.Location); err == nil && int64(free) < req.FreeSpaceFloor {
			res := domain.BackupResult{
				Outcome:      domain.PhaseFailed,
				ErrorClass:   domain.ErrorResourceExhausted,
				ErrorMessage: "insufficient free space for backup",
			}
			return s.finish(ctx, attempt, res, domain.JobFailed)
		}
	}

	missing := missingRoots(req.SourceRoots)
	if len(missing) > 0 {
		res := domain.BackupResult{
			Outcome:      domain.PhaseFailed,
			ErrorClass:   domain.ErrorSourceMissing,
			ErrorMessage: "required source roots missing",
			MissingRoots: missing,
		}
		return s.finish(ctx, attempt, res, domain.JobFailed)
	}

	var expectedFiles []corpus.File
	var expectedErr error
	if s.ManifestDir != "" {
		expectedFiles, expectedErr = hashSourceRoots(req.SourceRoots)
	}

	attempt.Phase = domain.PhaseBackingUp
	if err := s.Journal.UpdateAttempt(ctx, attempt); err != nil {
		return domain.BackupResult{Outcome: domain.PhaseFailed, ErrorClass: domain.ErrorInternal}, err
	}

	res, err := s.Engine.Backup(ctx, req)
	if err != nil && res.Outcome == "" {
		res.Outcome = domain.PhaseFailed
		res.ErrorClass = domain.ErrorInternal
		res.ErrorMessage = err.Error()
	}
	res = evaluateBackup(req, res)
	if res.Outcome == domain.PhaseSucceeded {
		s.recordExpectedManifest(req, &res, expectedFiles, expectedErr)
	}
	// Cancellation wins until the application starts its terminal commit. A
	// snapshot may remain useful for recovery, but cannot satisfy freshness.
	if ctx.Err() != nil {
		res.Outcome = domain.PhaseCancelled
		res.ErrorClass = domain.ErrorCancelled
		if res.SnapshotID != "" {
			res.Outcome = domain.PhasePartial
			res.PublishedNotGreen = true
		}
	}
	jobState := jobStateFromAttempt(res.Outcome)
	return s.finish(ctx, attempt, res, jobState)
}

func (s *Service) Restore(ctx context.Context, req domain.RestoreRequest) (domain.RestoreResult, error) {
	if req.StagingRoot == "" || req.SnapshotID == "" {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorConfig, ErrorMessage: "staging root and snapshot required"}, domain.ErrRestorePathRejected
	}
	if req.JobID == "" {
		req.JobID = domain.NewJobID()
	}
	hash, payload, err := domain.RestoreIdentityHash(req)
	if err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorConfig, ErrorMessage: err.Error()}, err
	}

	existing, err := s.Journal.GetJob(ctx, req.JobID)
	if err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}, err
	}
	if existing != nil {
		return s.restoreIdempotent(ctx, existing, hash)
	}

	if err := s.lockOp(); err != nil {
		existing, err2 := s.Journal.GetJob(ctx, req.JobID)
		if err2 == nil && existing != nil {
			return s.restoreIdempotent(ctx, existing, hash)
		}
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}, err
	}
	defer s.unlockOp()
	ctx, cancelOp := s.bindOpCancel(ctx)
	defer cancelOp()

	existing, err = s.Journal.GetJob(ctx, req.JobID)
	if err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}, err
	}
	if existing != nil {
		return s.restoreIdempotent(ctx, existing, hash)
	}

	if req.AttemptID == "" {
		req.AttemptID = domain.NewAttemptID()
	}

	stage, err := restore.Prepare(req.StagingRoot, req.JobID)
	if err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorPathSafety, ErrorMessage: err.Error()}, err
	}
	req.DestinationDir = stage.Path
	if err := req.Validate(); err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorConfig, ErrorMessage: err.Error()}, err
	}

	job := ports.JobRecord{
		ID:          req.JobID,
		Kind:        domain.JobRestore,
		State:       domain.JobActive,
		RequestHash: hash,
		RequestJSON: payload,
	}
	attempt := ports.AttemptRecord{
		ID:        req.AttemptID,
		JobID:     req.JobID,
		AttemptNo: 1,
		Kind:      domain.JobRestore,
		Phase:     domain.PhasePreparing,
		IntentAt:  s.now(),
		StartedAt: s.now(),
	}
	if err := s.Journal.BeginJob(ctx, job, attempt); err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: domain.ErrIntentNotPersisted.Error()}, domain.ErrIntentNotPersisted
	}

	if err := restore.Revalidate(stage); err != nil {
		res := domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorPathSafety, ErrorMessage: err.Error()}
		return s.finishRestore(attempt, res, domain.JobFailed)
	}

	req.VerifyContent = true
	res, err := s.Engine.Restore(ctx, req)
	if err != nil && res.State == "" {
		res.State = domain.RestoreFailed
		res.ErrorMessage = err.Error()
	}
	if ctx.Err() != nil {
		res.State = domain.RestoreCancelled
		res.ErrorClass = domain.ErrorCancelled
	} else if err != nil || !res.VerifiedByEngine || res.State != domain.RestoreVerifying {
		if res.State != domain.RestoreCancelled {
			res.State = domain.RestoreFailed
			res.ErrorClass = domain.ErrorIntegrity
		}
	}
	if res.State == domain.RestoreVerifying && res.VerifiedByEngine {
		if err := restore.Revalidate(stage); err != nil {
			res.State = domain.RestoreFailed
			res.ErrorClass = domain.ErrorPathSafety
			res.ErrorMessage = err.Error()
		} else {
			manPath, _, mErr := restore.WriteManifest(stage, req.SnapshotID, res.VerifiedByEngine)
			if mErr != nil {
				res.State = domain.RestoreFailed
				res.ErrorClass = domain.ErrorIntegrity
				res.ErrorMessage = mErr.Error()
			} else {
				res.ManifestPath = manPath
				res.Destination = stage.Path
				res.State = domain.RestoreReady
				res.IndependentOK = IndependentRestoreVerify(s.ManifestDir, string(req.SnapshotID), stage.Path) == nil
			}
		}
	}
	jobState := domain.JobFailed
	if res.State == domain.RestoreReady {
		jobState = domain.JobSucceeded
	}
	if res.State == domain.RestoreCancelled {
		jobState = domain.JobCancelled
	}
	return s.finishRestore(attempt, res, jobState)
}

func (s *Service) restoreIdempotent(ctx context.Context, existing *ports.JobRecord, hash string) (domain.RestoreResult, error) {
	if existing.RequestHash != "" && existing.RequestHash != hash {
		return domain.RestoreResult{
			State:        domain.RestoreFailed,
			ErrorClass:   domain.ErrorConfig,
			ErrorMessage: domain.ErrRestoreConflict.Error(),
		}, domain.ErrRestoreConflict
	}
	att, err := s.Journal.LatestAttempt(ctx, existing.ID)
	if err != nil {
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorInternal, ErrorMessage: err.Error()}, err
	}
	if existing.State == domain.JobActive || existing.State == domain.JobReconciling {
		res := domain.RestoreResult{State: domain.RestoreRestoring, ErrorMessage: "restore already in progress"}
		if att != nil && att.SummaryJSON != "" {
			_ = json.Unmarshal([]byte(att.SummaryJSON), &res)
			res.State = domain.RestoreRestoring
		}
		return res, nil
	}
	if att != nil && att.SummaryJSON != "" {
		var stored domain.RestoreResult
		if err := json.Unmarshal([]byte(att.SummaryJSON), &stored); err == nil && stored.State != "" {
			return stored, nil
		}
	}
	switch existing.State {
	case domain.JobSucceeded:
		return domain.RestoreResult{State: domain.RestoreReady}, nil
	case domain.JobCancelled:
		return domain.RestoreResult{State: domain.RestoreCancelled, ErrorClass: domain.ErrorCancelled}, nil
	default:
		return domain.RestoreResult{State: domain.RestoreFailed, ErrorClass: domain.ErrorIntegrity, ErrorMessage: "prior restore did not reach READY"}, nil
	}
}

func (s *Service) finishRestore(attempt ports.AttemptRecord, res domain.RestoreResult, jobState domain.JobState) (domain.RestoreResult, error) {
	b, _ := json.Marshal(res)
	outcome := domain.PhaseFailed
	if res.State == domain.RestoreReady {
		outcome = domain.PhaseSucceeded
	}
	if res.State == domain.RestoreCancelled {
		outcome = domain.PhaseCancelled
	}
	attempt.Phase = outcome
	attempt.Outcome = outcome
	attempt.SummaryJSON = string(b)
	attempt.ErrorClass = res.ErrorClass
	commitCtx, cancel := terminalContext()
	defer cancel()
	if err := s.Journal.CompleteAttempt(commitCtx, attempt, jobState); err != nil {
		res.State = domain.RestoreFailed
		res.ErrorClass = domain.ErrorInternal
		res.ErrorMessage = "terminal journal commit failed"
		return res, err
	}
	return res, nil
}

func (s *Service) recordExpectedManifest(req domain.BackupRequest, res *domain.BackupResult, files []corpus.File, hashErr error) {
	if s.ManifestDir == "" || res.SnapshotID == "" || hashErr != nil || len(files) == 0 {
		return
	}
	path, hash, err := WriteExpectedManifest(s.ManifestDir, expectedMeta{
		SnapshotID: string(res.SnapshotID),
		JobID:      string(req.JobID),
		AttemptID:  string(req.AttemptID),
		Roots:      req.SourceRoots,
		Files:      files,
	})
	if err != nil {
		return
	}
	res.ExpectedManifestPath = path
	res.ExpectedManifestSHA256 = hash
}

func (s *Service) Reconcile(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) ([]ports.AttemptRecord, error) {
	incomplete, err := s.Journal.IncompleteAttempts(ctx)
	if err != nil {
		return nil, err
	}
	var done []ports.AttemptRecord
	for _, rec := range incomplete {
		resolved := rec
		if !rec.Kind.IsBackupWork() {
			// An interrupted restore/check/browse cannot be reconciled from
			// backup snapshot tags, and must never block the backup scheduler
			// (this runs on every controller tick). Record it terminally;
			// recovering a partial restore is a separate operator-initiated
			// action (architecture section 20). A completed restore is never
			// inferred from process disappearance.
			resolved.Phase = domain.PhaseFailed
			resolved.Outcome = domain.PhaseFailed
			resolved.ErrorClass = domain.ErrorInternal
			resolved.EndedAt = s.now()
			if err := s.Journal.CompleteAttempt(ctx, resolved, domain.JobFailed); err != nil {
				return done, err
			}
			done = append(done, resolved)
			continue
		}
		resolved.Phase = domain.PhaseReconciling
		if err := s.Journal.UpdateAttempt(ctx, resolved); err != nil {
			return done, err
		}
		if rec.SnapshotID != "" {
			snap, err := s.Engine.ReadSnapshot(ctx, repo, password, rec.SnapshotID)
			if err == nil && snap != nil {
				resolved.Phase = domain.PhasePartial
				resolved.Outcome = domain.PhasePartial
				resolved.ErrorClass = domain.ErrorInternal
				resolved.EndedAt = s.now()
				if err := s.Journal.CompleteAttempt(ctx, resolved, domain.JobPartial); err != nil {
					return done, err
				}
				done = append(done, resolved)
				continue
			}
		}
		snaps, err := s.Engine.Snapshots(ctx, repo, password)
		if err != nil {
			return done, err
		}
		found := findTagged(snaps, rec)
		if found != nil {
			resolved.SnapshotID = found.ID
			resolved.Phase = domain.PhasePartial
			resolved.Outcome = domain.PhasePartial
			resolved.EndedAt = s.now()
			if err := s.Journal.CompleteAttempt(ctx, resolved, domain.JobPartial); err != nil {
				return done, err
			}
			done = append(done, resolved)
			continue
		}
		resolved.Phase = domain.PhaseFailed
		resolved.Outcome = domain.PhaseFailed
		resolved.ErrorClass = domain.ErrorEngineCrash
		resolved.EndedAt = s.now()
		if err := s.Journal.CompleteAttempt(ctx, resolved, domain.JobFailed); err != nil {
			return done, err
		}
		done = append(done, resolved)
	}
	return done, nil
}

func terminalContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (s *Service) finish(ctx context.Context, attempt ports.AttemptRecord, res domain.BackupResult, jobState domain.JobState) (domain.BackupResult, error) {
	ctx, cancel := terminalContext()
	defer cancel()
	b, _ := json.Marshal(res)
	res.JobID = attempt.JobID
	res.AttemptID = attempt.ID
	if attempt.Kind == domain.JobQualificationBackup {
		res.AttemptClass = domain.AttemptQualification
	} else if res.AttemptClass == "" {
		res.AttemptClass = domain.AttemptScheduled
	}
	attempt.Phase = res.Outcome
	attempt.Outcome = res.Outcome
	attempt.SnapshotID = res.SnapshotID
	attempt.ErrorClass = res.ErrorClass
	attempt.SummaryJSON = string(b)
	attempt.EndedAt = s.now()
	if err := s.Journal.CompleteAttempt(ctx, attempt, jobState); err != nil {
		res.CleanupOK = false
		if res.Outcome == domain.PhaseSucceeded {
			res.Outcome = domain.PhasePartial
			res.PublishedNotGreen = true
			res.ErrorClass = domain.ErrorInternal
			res.ErrorMessage = "durable journal commit failed after engine result"
			jobState = domain.JobPartial
			attempt.Phase = res.Outcome
			attempt.Outcome = res.Outcome
			attempt.ErrorClass = res.ErrorClass
			_ = s.Journal.CompleteAttempt(ctx, attempt, jobState)
		}
		return res, err
	}
	if err := s.Journal.AppendOutbox(ctx, ports.OutboxEvent{
		EventID:   domain.NewUUID(),
		AttemptID: attempt.ID,
		Type:      "backup.terminal",
		Payload:   string(b),
		CreatedAt: s.now(),
	}); err != nil {
		// The terminal result is durable; delivery can be reconstructed from it.
		// Do not mislabel a delivery failure as VSS cleanup success/failure.
		return res, err
	}
	if res.Outcome == domain.PhaseSucceeded && res.SnapshotID != "" {
		catalogPayload, _ := json.Marshal(map[string]string{"snapshot_id": string(res.SnapshotID)})
		if err := s.Journal.AppendOutbox(ctx, ports.OutboxEvent{
			EventID:   domain.NewUUID(),
			AttemptID: attempt.ID,
			Type:      "catalog.build",
			Payload:   string(catalogPayload),
			CreatedAt: s.now(),
		}); err != nil {
			// The backup itself is durable and already queued for report;
			// a catalog is a convenience layered on top of it, not a
			// condition of backup success -- do not fail the backup over this.
			_ = err
		}
	}
	return res, nil
}

func missingRoots(roots []string) []string {
	var missing []string
	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			missing = append(missing, r)
		}
	}
	return missing
}

func findTagged(snaps []ports.Snapshot, rec ports.AttemptRecord) *ports.Snapshot {
	want := "attempt:" + string(rec.ID)
	for i := range snaps {
		for _, t := range snaps[i].Tags {
			if t == want {
				return &snaps[i]
			}
		}
	}
	return nil
}

func jobStateFromAttempt(p domain.AttemptPhase) domain.JobState {
	switch p {
	case domain.PhaseSucceeded:
		return domain.JobSucceeded
	case domain.PhasePartial:
		return domain.JobPartial
	case domain.PhaseCancelled:
		return domain.JobCancelled
	default:
		return domain.JobFailed
	}
}

func evaluateBackup(req domain.BackupRequest, res domain.BackupResult) domain.BackupResult {
	if res.Outcome == domain.PhaseCancelled {
		return res
	}
	if !res.ExitKnown {
		res.Outcome = domain.PhaseFailed
		res.ErrorClass = domain.ErrorInternal
		res.ErrorMessage = domain.ErrUnknownExitCode.Error()
		res.PublishedNotGreen = res.SnapshotID != ""
		return res
	}
	if res.Outcome == domain.PhaseSucceeded {
		if res.SnapshotID == "" || !res.ReadbackOK {
			res.Outcome = domain.PhaseFailed
			res.ErrorClass = domain.ErrorIntegrity
			res.PublishedNotGreen = true
			return res
		}
		if req.VSSMode == domain.VSSRequired && res.Consistency != domain.ConsistencyVerified {
			res.Outcome = domain.PhasePartial
			res.ErrorClass = domain.ErrorConsistencyNotMet
			res.PublishedNotGreen = true
			return res
		}
		if res.Summary.MessageSeen == false {
			res.Outcome = domain.PhaseFailed
			res.ErrorClass = domain.ErrorIntegrity
			res.ErrorMessage = "missing restic terminal summary"
			return res
		}
	}
	return res
}
