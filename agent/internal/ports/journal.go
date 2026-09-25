package ports

import (
	"context"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// AttemptRecord is durable execution state.
type AttemptRecord struct {
	ID               domain.AttemptID
	JobID            domain.JobID
	AttemptNo        int
	Kind             domain.JobKind
	SlotKey          string
	PolicyRevisionID string
	Phase            domain.AttemptPhase
	Outcome          domain.AttemptPhase
	SnapshotID       domain.SnapshotID
	ErrorClass       domain.ErrorClass
	SummaryJSON      string
	EvidenceJSON     string
	IntentAt         time.Time
	StartedAt        time.Time
	EndedAt          time.Time
}

type JobRecord struct {
	ID               domain.JobID
	Kind             domain.JobKind
	SlotKey          string
	PolicyRevisionID string
	State            domain.JobState
	RequestHash      string
	RequestJSON      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type OutboxEvent struct {
	EventID   string
	AttemptID domain.AttemptID
	Seq       int
	Type      string
	Payload   string
	CreatedAt time.Time
	AckedAt   time.Time
}

// Journal is the local durable ledger. Failure to persist intent MUST prevent launch.
type Journal interface {
	Open() error
	Close() error
	BeginJob(ctx context.Context, job JobRecord, attempt AttemptRecord) error
	UpdateAttempt(ctx context.Context, rec AttemptRecord) error
	CompleteAttempt(ctx context.Context, rec AttemptRecord, jobState domain.JobState) error
	GetAttempt(ctx context.Context, id domain.AttemptID) (*AttemptRecord, error)
	GetJob(ctx context.Context, id domain.JobID) (*JobRecord, error)
	JobBySlot(ctx context.Context, slotKey string) (*JobRecord, error)
	IncompleteAttempts(ctx context.Context) ([]AttemptRecord, error)
	ConsumeCommand(ctx context.Context, commandID, jobID string, terminal bool) (already bool, err error)
	CommandConsumed(ctx context.Context, commandID string) (bool, error)
	// CommandResult returns the durable outcome of a previously-dispatched
	// command, including the exact "extra" result payload (e.g. a
	// BROWSE_LOCAL_DIR listing or an APPLY_SELECTION confirmation) so a
	// redelivered command can replay its ack instead of re-running the
	// handler and losing that payload.
	CommandResult(ctx context.Context, commandID string) (terminal bool, state string, extra map[string]any, err error)
	FinishCommand(ctx context.Context, commandID, state string, extra map[string]any) error
	AppendOutbox(ctx context.Context, ev OutboxEvent) error
	UnackedOutbox(ctx context.Context, limit int) ([]OutboxEvent, error)
	AckOutbox(ctx context.Context, eventID string) error
	SavePolicy(ctx context.Context, p domain.LocalPolicy) error
	LatestPolicy(ctx context.Context) (*domain.LocalPolicy, error)
	LatestSucceededBackupSlot(ctx context.Context) (string, error)
	LatestAttempt(ctx context.Context, jobID domain.JobID) (*AttemptRecord, error)
	RecentAttempts(ctx context.Context, limit int) ([]AttemptRecord, error)
	BeginAttempt(ctx context.Context, rec AttemptRecord) error
	SetJobState(ctx context.Context, id domain.JobID, state domain.JobState) error
}
