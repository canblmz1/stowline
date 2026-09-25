package domain

// ConsistencyClass is the source-consistency claim for a backup attempt.
// Exit code 0 is never sufficient to infer VSS success.
type ConsistencyClass string

const (
	ConsistencyVerified ConsistencyClass = "CONSISTENCY_VERIFIED"
	ConsistencyNotMet   ConsistencyClass = "CONSISTENCY_NOT_MET"
	LiveReadAllowed     ConsistencyClass = "LIVE_READ_ALLOWED"
)

// VSSMode is the required consistency policy for local roots.
type VSSMode string

const (
	VSSRequired VSSMode = "required"
	VSSDisabled VSSMode = "disabled"
)

// VolumeEvidence is per-volume VSS observation bound to a pinned Restic version.
type VolumeEvidence struct {
	Volume             string
	CreatingSeen       bool
	SuccessSeen        bool
	FailureSeen        bool
	FailureMessage     string
	ExcludedByUser     bool
	LiveFallbackLikely bool
}

// VSSEvidence is the complete consistency picture for one engine invocation.
type VSSEvidence struct {
	EngineVersion       string
	ParserBoundTo       string
	Mode                VSSMode
	Volumes             []VolumeEvidence
	RawMatchedLines     []string
	PositiveAllRequired bool
	ParserNotes         []string
}

func (e VSSEvidence) Class() ConsistencyClass {
	switch e.Mode {
	case VSSDisabled:
		return LiveReadAllowed
	case VSSRequired:
		if e.PositiveAllRequired {
			return ConsistencyVerified
		}
		return ConsistencyNotMet
	default:
		return ConsistencyNotMet
	}
}
