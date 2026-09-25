package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// RestoreRequest is an immutable typed restore. Original-location is rejected in this phase.
type RestoreRequest struct {
	JobID                JobID
	AttemptID            AttemptID
	CorrelationID        CorrelationID
	SnapshotID           SnapshotID
	Selections           []string
	StagingRoot          string
	DestinationDir       string
	ConflictMode         ConflictMode
	PasswordRef          SecretRef
	Repository           RepositoryDescriptor
	VerifyContent        bool
	Deadline             time.Time
	BandwidthKiBps       int
	RestoreDownloadKiBps int
	// Progress receives counts only (never file names). Not part of the
	// request identity: it is how a caller watches, not what is restored.
	Progress func(RestoreProgress) `json:"-"`
}

// RestoreProgress mirrors BackupProgress for a running restore.
type RestoreProgress struct {
	PercentDone float64
	FilesDone   int64
	TotalFiles  int64
	BytesDone   int64
	TotalBytes  int64
}

type ConflictMode string

const (
	ConflictFailIfExists   ConflictMode = "FAIL_IF_EXISTS"
	ConflictRenameRestored ConflictMode = "RENAME_RESTORED"
)

type RestoreResult struct {
	State            RestoreState
	ErrorClass       ErrorClass
	ErrorMessage     string
	ExitCode         int
	VerifiedByEngine bool
	IndependentOK    bool
	ManifestPath     string
	Destination      string
	Duration         time.Duration
}

func (r RestoreRequest) Validate() error {
	if err := r.Repository.Validate(); err != nil {
		return err
	}
	if r.SnapshotID == "" {
		return fmtBackupErr("full snapshot id required")
	}
	if len(r.SnapshotID) != 64 {
		return fmtBackupErr("snapshot id must be the full 64-character hash")
	}
	if r.StagingRoot == "" || r.DestinationDir == "" {
		return fmtBackupErr("staging root and destination required")
	}
	for _, s := range r.Selections {
		if looksLikeFlag(s) {
			return ErrArbitraryFlags
		}
	}
	return nil
}

type restoreIdentity struct {
	BackendKind  string   `json:"backend_kind"`
	Location     string   `json:"location"`
	GenerationID string   `json:"generation_id"`
	DeviceID     string   `json:"device_id"`
	SnapshotID   string   `json:"snapshot_id"`
	Selections   []string `json:"selections"`
	StagingRoot  string   `json:"staging_root"`
	ConflictMode string   `json:"conflict_mode"`
	Verify       bool     `json:"verify"`
}

// RestoreIdentityHash is a secret-free digest of the immutable restore request.
func RestoreIdentityHash(r RestoreRequest) (hash string, payload string, err error) {
	id := restoreIdentity{
		BackendKind:  string(r.Repository.BackendKind),
		Location:     r.Repository.Location,
		GenerationID: r.Repository.GenerationID,
		DeviceID:     r.Repository.DeviceID,
		SnapshotID:   string(r.SnapshotID),
		Selections:   append([]string(nil), r.Selections...),
		StagingRoot:  r.StagingRoot,
		ConflictMode: string(r.ConflictMode),
		Verify:       true,
	}
	if id.Selections == nil {
		id.Selections = []string{}
	}
	sort.Strings(id.Selections)
	b, err := json.Marshal(id)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), string(b), nil
}
