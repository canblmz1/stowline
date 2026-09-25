package domain

// JobKind is the durable intent kind. Restore and backup do not share one status enum.
type JobKind string

const (
	JobBackup              JobKind = "BACKUP"
	JobQualificationBackup JobKind = "QUALIFICATION_BACKUP"
	JobRestore             JobKind = "RESTORE"
	JobBrowse              JobKind = "BROWSE"
	JobCheck               JobKind = "CHECK"
)

func (k JobKind) IsBackupWork() bool {
	return k == JobBackup || k == JobQualificationBackup
}

// JobState is durable job intent, not process observation.
type JobState string

const (
	JobQueued      JobState = "QUEUED"
	JobActive      JobState = "ACTIVE"
	JobBackoff     JobState = "BACKOFF"
	JobReconciling JobState = "RECONCILING"
	JobSucceeded   JobState = "SUCCEEDED"
	JobPartial     JobState = "PARTIAL"
	JobFailed      JobState = "FAILED"
	JobCancelled   JobState = "CANCELLED"
	JobExpired     JobState = "EXPIRED"
)

// AttemptPhase is observed execution for one attempt.
type AttemptPhase string

const (
	PhaseQueued       AttemptPhase = "QUEUED"
	PhasePreparing    AttemptPhase = "PREPARING"
	PhaseVSSPreparing AttemptPhase = "VSS_PREPARING"
	PhaseBackingUp    AttemptPhase = "BACKING_UP"
	PhaseVerifying    AttemptPhase = "VERIFYING"
	PhaseFinalizing   AttemptPhase = "FINALIZING"
	PhaseSucceeded    AttemptPhase = "SUCCEEDED"
	PhasePartial      AttemptPhase = "PARTIAL"
	PhaseFailed       AttemptPhase = "FAILED"
	PhaseCancelling   AttemptPhase = "CANCELLING"
	PhaseCancelled    AttemptPhase = "CANCELLED"
	PhaseInterrupted  AttemptPhase = "INTERRUPTED"
	PhaseReconciling  AttemptPhase = "RECONCILING"
)

// RestoreState is the restore request/execution machine. It is not a backup status.
type RestoreState string

const (
	RestoreRequested             RestoreState = "REQUESTED"
	RestorePlanning              RestoreState = "PLANNING"
	RestoreAwaitingAuthorization RestoreState = "AWAITING_AUTHORIZATION"
	RestoreQueued                RestoreState = "QUEUED"
	RestorePreparing             RestoreState = "PREPARING"
	RestoreRestoring             RestoreState = "RESTORING"
	RestoreVerifying             RestoreState = "VERIFYING"
	RestoreReady                 RestoreState = "READY"
	RestoreCompleted             RestoreState = "COMPLETED"
	RestoreFailed                RestoreState = "FAILED"
	RestoreCancelled             RestoreState = "CANCELLED"
	RestoreCancelling            RestoreState = "CANCELLING"
	RestoreExpired               RestoreState = "EXPIRED"
)

func (p AttemptPhase) Terminal() bool {
	switch p {
	case PhaseSucceeded, PhasePartial, PhaseFailed, PhaseCancelled:
		return true
	default:
		return false
	}
}

func (s JobState) Terminal() bool {
	switch s {
	case JobSucceeded, JobPartial, JobFailed, JobCancelled, JobExpired:
		return true
	default:
		return false
	}
}
