package domain

import "errors"

var (
	ErrMaintenanceForbidden  = errors.New("endpoint must not perform repository maintenance")
	ErrUnknownExitCode       = errors.New("unknown restic exit code is a failure")
	ErrPartialNotSuccess     = errors.New("restic partial outcome cannot be SUCCESS")
	ErrMissingSnapshotID     = errors.New("backup did not produce an exact snapshot id")
	ErrSnapshotReadback      = errors.New("published snapshot could not be read back")
	ErrConsistencyNotMet     = errors.New("required consistency evidence was not met")
	ErrIntentNotPersisted    = errors.New("durable intent could not be recorded; refusing to launch")
	ErrOperationInProgress   = errors.New("another backup, restore, or check is already active")
	ErrArbitraryFlags        = errors.New("engine does not accept arbitrary flags or executable paths")
	ErrForbiddenCommand      = errors.New("command is not in the endpoint allowlist")
	ErrSecretInArguments     = errors.New("refusing to put a secret in process arguments")
	ErrRestorePathRejected   = errors.New("restore destination failed path safety checks")
	ErrStagingEscape         = errors.New("restore path escapes the approved staging root")
	ErrBinaryDigestMismatch  = errors.New("pinned executable digest mismatch")
	ErrBinaryVersionMismatch = errors.New("pinned executable version mismatch")
	ErrRepositoryBinding     = errors.New("repository binding does not match expected identity")
	ErrCapabilityUnsupported = errors.New("storage profile lacks a required capability")
	ErrShellInvocation       = errors.New("shell invocation is forbidden")
	ErrRemoteExecutable      = errors.New("refusing remotely supplied executable path or script")
	ErrTLSVerification       = errors.New("TLS verification must not be disabled")
	ErrIncompleteStaging     = errors.New("restore staging is incomplete")
	ErrOriginalLocation      = errors.New("original-location restore is out of scope")
	ErrUnlockAutomatic       = errors.New("automatic repository unlock is forbidden")
	ErrConfig                = errors.New("invalid configuration")
	ErrAuth                  = errors.New("repository authentication failed")
	ErrSecretScopeMismatch   = errors.New("secret blob scope does not match the requested provider")
	ErrRestoreConflict       = errors.New("restore job id already exists with a different request")
	ErrPolicyInvalid         = errors.New("policy is invalid")
	ErrBrowsePathRejected    = errors.New("browse path failed safety checks")
	ErrSelectionRejected     = errors.New("selection failed validation")
	// ErrCatalogEnumerationFailed marks a fatal, agent-detected failure while
	// building a snapshot's permanent catalog (restic itself could not read
	// the snapshot -- auth or corruption). The automatic post-backup outbox
	// path and the on-demand BUILD_SNAPSHOT_CATALOG command need opposite
	// reactions to this exact error (the outbox must swallow it so a
	// permanently broken snapshot doesn't retry forever; the command must
	// propagate it so the operator sees FAILED) -- this sentinel is what
	// lets each caller tell the two apart via errors.Is.
	ErrCatalogEnumerationFailed = errors.New("catalog enumeration failed")
	// ErrWANAdmissionDeclined marks an operator-triggered backup that never
	// started because WAN admission (pause/provider/policy/site capacity)
	// declined it. The scheduled/automatic path treats the same decline as
	// a normal "wait my turn" outcome, not an error -- this exists so an
	// explicit "Backup Now" click can tell the difference from a real
	// completed backup instead of silently acking SUCCEEDED for nothing.
	ErrWANAdmissionDeclined = errors.New("WAN admission declined; backup did not start")
)

// WANAdmissionDeclinedError carries WHICH short, fixed admission-status
// token declined an operator-triggered backup (e.g. PAUSED,
// PROVIDER_UNAVAILABLE, SITE_UNMEASURED, or an AcquireLease status like
// SITE_CAPACITY_WAIT) -- never a free-text message, so it is always safe
// to put in a diagnostic log line. errors.Is(err, ErrWANAdmissionDeclined)
// still matches it.
type WANAdmissionDeclinedError struct {
	Reason string
}

func (e *WANAdmissionDeclinedError) Error() string {
	return "WAN admission declined; backup did not start: " + e.Reason
}

func (e *WANAdmissionDeclinedError) Is(target error) bool {
	return target == ErrWANAdmissionDeclined
}
