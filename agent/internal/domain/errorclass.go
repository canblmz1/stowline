package domain

// ErrorClass is a stable, non-secret classification for logs and alerts.
type ErrorClass string

const (
	ErrorNetwork           ErrorClass = "NETWORK"
	ErrorProviderRateLimit ErrorClass = "PROVIDER_RATE_LIMIT"
	ErrorProviderQuota     ErrorClass = "PROVIDER_QUOTA"
	ErrorAuth              ErrorClass = "AUTH"
	ErrorSourceMissing     ErrorClass = "SOURCE_MISSING"
	ErrorSourceUnreadable  ErrorClass = "SOURCE_UNREADABLE"
	ErrorConsistencyNotMet ErrorClass = "CONSISTENCY_NOT_MET"
	ErrorVSS               ErrorClass = "VSS"
	ErrorRepositoryLocked  ErrorClass = "REPOSITORY_LOCKED"
	ErrorIntegrity         ErrorClass = "INTEGRITY"
	ErrorResourceExhausted ErrorClass = "RESOURCE_EXHAUSTED"
	ErrorEngineCrash       ErrorClass = "ENGINE_CRASH"
	ErrorCancelled         ErrorClass = "CANCELLED"
	ErrorInternal          ErrorClass = "INTERNAL"
	ErrorPathSafety        ErrorClass = "PATH_SAFETY"
	ErrorConfig            ErrorClass = "CONFIG"
	ErrorPreflight         ErrorClass = "PREFLIGHT"
)
