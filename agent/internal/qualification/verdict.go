package qualification

import "github.com/canblmz1/stowline/agent/internal/scheduler"

const (
	Pass                  = "PASS"
	ProductFailure        = "PRODUCT_FAILURE"
	QualificationFailure  = "QUALIFICATION_FAILURE"
	BlockedByScheduleSlot = "BLOCKED_BY_SCHEDULE_SLOT"
	BlockedExternal       = "BLOCKED_EXTERNAL"
)

// ClassifyIdleAfterServiceStart maps a scheduler tick that did not start a
// backup. A consumed civil-day slot is not a product defect.
func ClassifyIdleAfterServiceStart(schedulerReason string) string {
	switch schedulerReason {
	case scheduler.ReasonSkipComplete:
		return BlockedByScheduleSlot
	default:
		return QualificationFailure
	}
}

// FinalReportLabel is the operator-facing freeze label for a classification code.
func FinalReportLabel(code string) string {
	switch code {
	case Pass:
		return "FULL PILOT DEBUG-QUALIFIED — EXTERNAL OAUTH/SOAK REMAIN"
	case ProductFailure:
		return "NOT READY"
	case BlockedByScheduleSlot, QualificationFailure, BlockedExternal:
		return "QUALIFICATION BLOCKED"
	default:
		return "QUALIFICATION BLOCKED"
	}
}
