package qualification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const RequestSchema = "stowline.qualification.request.v1"
const ResultSchema = "stowline.qualification.result.v1"

const OperationBackup = "BACKUP"

// Request is the only accepted qualification control document.
// Unknown fields are rejected. Paths, repository URLs, and argv are forbidden.
type Request struct {
	Schema             string `json:"schema"`
	QualificationRunID string `json:"qualification_run_id"`
	Operation          string `json:"operation"`
	SyntheticCorpus    bool   `json:"synthetic_corpus"`
}

type Result struct {
	Schema             string              `json:"schema"`
	QualificationRunID string              `json:"qualification_run_id"`
	AttemptClass       domain.AttemptClass `json:"attempt_class"`
	Outcome            domain.AttemptPhase `json:"outcome"`
	ErrorClass         domain.ErrorClass   `json:"error_class,omitempty"`
	ErrorMessage       string              `json:"error_message,omitempty"`
	SnapshotID         domain.SnapshotID   `json:"snapshot_id,omitempty"`
	JobID              domain.JobID        `json:"job_id,omitempty"`
	AttemptID          domain.AttemptID    `json:"attempt_id,omitempty"`
	PublishedNotGreen  bool                `json:"published_not_green,omitempty"`
	Replay             bool                `json:"replay,omitempty"`
}

func ParseRequest(raw []byte) (Request, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("%w: qualification request: %v", domain.ErrConfig, err)
	}
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (r Request) Validate() error {
	if r.Schema != RequestSchema {
		return fmt.Errorf("%w: qualification request schema must be %s", domain.ErrConfig, RequestSchema)
	}
	if err := domain.ValidateQualificationRunID(r.QualificationRunID); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrConfig, err)
	}
	if r.Operation != OperationBackup {
		return fmt.Errorf("%w: qualification operation %q is forbidden", domain.ErrForbiddenCommand, r.Operation)
	}
	if !r.SyntheticCorpus {
		return fmt.Errorf("%w: qualification backup requires synthetic_corpus=true", domain.ErrConfig)
	}
	return nil
}

func BoundBackupRequest(runID string, src []string, repo domain.RepositoryDescriptor, vss domain.VSSMode, password domain.SecretRef, deviceID, installationID, policyRev, cacheDir, host string, floor int64, bandwidth int) domain.BackupRequest {
	return domain.BackupRequest{
		JobID:              domain.QualificationJobID(runID),
		AttemptID:          domain.QualificationAttemptID(runID),
		DeviceID:           deviceID,
		InstallationID:     installationID,
		PolicyRevisionID:   policyRev,
		Repository:         repo,
		SourceRoots:        src,
		VSSMode:            vss,
		PasswordRef:        password,
		CacheDir:           cacheDir,
		Host:               host,
		SlotKey:            domain.QualificationSlotKey(runID),
		FreeSpaceFloor:     floor,
		BandwidthKiBps:     bandwidth,
		AttemptClass:       domain.AttemptQualification,
		QualificationRunID: runID,
	}
}

func ResultFromBackup(runID string, res domain.BackupResult, replay bool) Result {
	msg := res.ErrorMessage
	if replay && msg == "" {
		msg = "qualification run already completed"
	}
	return Result{
		Schema:             ResultSchema,
		QualificationRunID: runID,
		AttemptClass:       domain.AttemptQualification,
		Outcome:            res.Outcome,
		ErrorClass:         res.ErrorClass,
		ErrorMessage:       msg,
		SnapshotID:         res.SnapshotID,
		JobID:              res.JobID,
		AttemptID:          res.AttemptID,
		PublishedNotGreen:  res.PublishedNotGreen,
		Replay:             replay,
	}
}

func LooksLikeArbitraryCommand(op string) bool {
	u := strings.ToUpper(strings.TrimSpace(op))
	switch u {
	case "SHELL", "EXEC", "POWERSHELL", "CMD", "FORGET", "PRUNE", "UNLOCK", "REPAIR", "KEY", "RESTORE", "RUN_BACKUP":
		return true
	default:
		return false
	}
}
