package domain

// WANClass is the admission class for a site lease. Keep v1 small.
type WANClass string

const (
	WANClassNormalBackup WANClass = "NORMAL_BACKUP"
	WANClassInitialSeed  WANClass = "INITIAL_SEED"
	WANClassRestore      WANClass = "RESTORE"
	WANClassCanary       WANClass = "CANARY"
	WANClassMaintenance  WANClass = "MAINTENANCE"
)

func (c WANClass) Valid() bool {
	switch c {
	case WANClassNormalBackup, WANClassInitialSeed, WANClassRestore, WANClassCanary, WANClassMaintenance:
		return true
	default:
		return false
	}
}

// Admission status values. These are not terminal backup failures.
const (
	AdmissionGranted             = "GRANTED"
	AdmissionQueued              = "QUEUED"
	AdmissionSiteCapacityWait    = "SITE_CAPACITY_WAIT"
	AdmissionDeferred            = "DEFERRED"
	AdmissionProviderUnavailable = "PROVIDER_UNAVAILABLE"
	AdmissionPaused              = "PAUSED"
	AdmissionSiteUnmeasured      = "SITE_UNMEASURED"
	AdmissionControlUnavailable  = "CONTROL_UNAVAILABLE"
	AdmissionDenied              = "DENIED"
)
