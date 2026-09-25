package domain

import (
	"strings"
	"time"
)

// BinaryPin is a locally installed, digest-verified executable. Remote config cannot change it.
type BinaryPin struct {
	Product string
	Version string
	Path    string
	SHA256  string
}

// BackupRequest is a typed engine request. It never carries arbitrary flags.
type BackupRequest struct {
	JobID              JobID
	AttemptID          AttemptID
	CorrelationID      CorrelationID
	DeviceID           string
	InstallationID     string
	PolicyRevisionID   string
	Repository         RepositoryDescriptor
	SourceRoots        []string
	OptionalRoots      []string
	Excludes           []string
	Tags               []string
	VSSMode            VSSMode
	PasswordRef        SecretRef
	CacheDir           string
	BandwidthKiBps     int
	WANClass           WANClass
	Deadline           time.Time
	Host               string
	SlotKey            string
	FreeSpaceFloor     int64
	AttemptClass       AttemptClass
	QualificationRunID string
	// Progress receives sanitized numeric backup progress only. Engine output,
	// paths and file contents must never cross this callback boundary.
	Progress func(BackupProgress)
}

// BackupProgress is safe to report to the control plane. PercentDone uses
// percentage points (0..100), matching the operator UI.
type BackupProgress struct {
	PercentDone float64
	FilesDone   int64
	TotalFiles  int64
	BytesDone   int64
	TotalBytes  int64
}

// BackupResult is the evaluated outcome. Process success is necessary but not sufficient.
type BackupResult struct {
	Outcome                AttemptPhase
	ErrorClass             ErrorClass
	ErrorMessage           string
	JobID                  JobID
	AttemptID              AttemptID
	ExitCode               int
	ExitKnown              bool
	SnapshotID             SnapshotID
	RepositoryID           string
	Summary                BackupSummary
	VSS                    VSSEvidence
	Consistency            ConsistencyClass
	RequiredRootsOK        bool
	MissingRoots           []string
	ReadbackOK             bool
	CleanupOK              bool
	PublishedNotGreen      bool
	Duration               time.Duration
	StartedAt              time.Time
	EndedAt                time.Time
	ExpectedManifestPath   string
	ExpectedManifestSHA256 string
	AttemptClass           AttemptClass
	QualificationRunID     string
}

// AttemptClass distinguishes scheduled freshness work from qualification-only backups.
type AttemptClass string

const (
	AttemptScheduled     AttemptClass = "SCHEDULED_BACKUP"
	AttemptQualification AttemptClass = "QUALIFICATION_BACKUP"
)

const QualificationSlotPrefix = "qualification|"

// ControlSlotPrefix marks a slot key as an operator-triggered "Backup Now"
// (vs. the automatic scheduler's own slot keys), so WAN classification can
// treat it as a normal-priority backup instead of competing for the
// server's single site-wide seed-admission slot merely because the device
// has no prior successful backup yet.
const ControlSlotPrefix = "control|"

func ControlSlotKey(deviceID string) string {
	return ControlSlotPrefix + deviceID
}

func IsControlSlotKey(slot string) bool {
	return strings.HasPrefix(slot, ControlSlotPrefix)
}

func (c AttemptClass) Effective() AttemptClass {
	if c == "" {
		return AttemptScheduled
	}
	return c
}

func (c AttemptClass) JobKind() JobKind {
	if c == AttemptQualification {
		return JobQualificationBackup
	}
	return JobBackup
}

func QualificationSlotKey(runID string) string {
	return QualificationSlotPrefix + runID
}

func QualificationJobID(runID string) JobID {
	return JobID("qual-job-" + runID)
}

func QualificationAttemptID(runID string) AttemptID {
	return AttemptID("qual-att-" + runID)
}

func IsQualificationSlotKey(slot string) bool {
	return strings.HasPrefix(slot, QualificationSlotPrefix)
}

func ValidateQualificationRunID(id string) error {
	if len(id) < 8 || len(id) > 64 {
		return fmtBackupErr("qualification_run_id must be 8-64 characters")
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmtBackupErr("qualification_run_id has invalid characters")
		}
	}
	return nil
}

// BackupSummary is normalized from Restic JSON. Unknown fields are ignored, not fatal.
type BackupSummary struct {
	FilesNew            int64
	FilesChanged        int64
	FilesUnmodified     int64
	DirsNew             int64
	DirsChanged         int64
	DirsUnmodified      int64
	DataAdded           int64
	DataAddedPacked     int64
	TotalFilesProcessed int64
	TotalBytesProcessed int64
	SnapshotID          string
	MessageSeen         bool
}

func (r BackupRequest) Validate() error {
	if r.Repository.Validate() != nil {
		return r.Repository.Validate()
	}
	if len(r.SourceRoots) == 0 {
		return fmtBackupErr("at least one source root is required")
	}
	for _, p := range r.SourceRoots {
		if looksLikeFlag(p) {
			return ErrArbitraryFlags
		}
	}
	for _, p := range r.Excludes {
		if looksLikeFlag(p) {
			return ErrArbitraryFlags
		}
	}
	if r.VSSMode == "" {
		return fmtBackupErr("vss_mode required")
	}
	if r.VSSMode != VSSRequired && r.VSSMode != VSSDisabled {
		return fmtBackupErr("vss_mode must be required or disabled")
	}
	switch r.AttemptClass {
	case "", AttemptScheduled:
		if IsQualificationSlotKey(r.SlotKey) {
			return fmtBackupErr("scheduled backup cannot use a qualification slot key")
		}
	case AttemptQualification:
		if err := ValidateQualificationRunID(r.QualificationRunID); err != nil {
			return err
		}
		if r.SlotKey != QualificationSlotKey(r.QualificationRunID) {
			return fmtBackupErr("qualification slot_key must be derived from qualification_run_id")
		}
	default:
		return fmtBackupErr("unknown attempt class")
	}
	return nil
}

func looksLikeFlag(s string) bool {
	return len(s) > 0 && s[0] == '-'
}

func fmtBackupErr(msg string) error {
	return &typedError{class: ErrorConfig, msg: msg}
}

type typedError struct {
	class ErrorClass
	msg   string
}

func (e *typedError) Error() string { return e.msg }
