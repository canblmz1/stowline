package restic

import "github.com/canblmz1/stowline/agent/internal/domain"

// Exit codes documented for Restic 0.19.1. Unknown codes are failures.
const (
	ExitOK            = 0
	ExitFailed        = 1
	ExitGoRuntime     = 2
	ExitPartial       = 3
	ExitRepoMissing   = 10
	ExitLockFailed    = 11
	ExitWrongPassword = 12
	ExitCancelled     = 130
)

type classifiedExit struct {
	Code    int
	Known   bool
	Partial bool
	OK      bool
	Class   domain.ErrorClass
	Fatal   bool
}

func classifyExit(code int) classifiedExit {
	switch code {
	case ExitOK:
		return classifiedExit{Code: code, Known: true, OK: true}
	case ExitPartial:
		return classifiedExit{Code: code, Known: true, Partial: true, Class: domain.ErrorSourceUnreadable, Fatal: false}
	case ExitFailed:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorInternal, Fatal: true}
	case ExitGoRuntime:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorEngineCrash, Fatal: true}
	case ExitRepoMissing:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorConfig, Fatal: true}
	case ExitLockFailed:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorRepositoryLocked, Fatal: true}
	case ExitWrongPassword:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorAuth, Fatal: true}
	case ExitCancelled:
		return classifiedExit{Code: code, Known: true, Class: domain.ErrorCancelled, Fatal: true}
	default:
		return classifiedExit{Code: code, Known: false, Class: domain.ErrorInternal, Fatal: true}
	}
}
