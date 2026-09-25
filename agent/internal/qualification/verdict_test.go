package qualification

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/scheduler"
)

func TestSkipCompleteIsBlockedByScheduleSlotNotProductFailure(t *testing.T) {
	got := ClassifyIdleAfterServiceStart(scheduler.ReasonSkipComplete)
	if got != BlockedByScheduleSlot {
		t.Fatalf("got %s", got)
	}
	if FinalReportLabel(got) != "QUALIFICATION BLOCKED" {
		t.Fatalf("label=%s", FinalReportLabel(got))
	}
	if FinalReportLabel(got) == "NOT READY" {
		t.Fatal("consumed daily slot must not be NOT READY")
	}
}

func TestUnknownIdleIsQualificationFailure(t *testing.T) {
	if ClassifyIdleAfterServiceStart("mystery") != QualificationFailure {
		t.Fatal("expected QUALIFICATION_FAILURE")
	}
}

func TestProductFailureLabelIsNotReady(t *testing.T) {
	if FinalReportLabel(ProductFailure) != "NOT READY" {
		t.Fatal(FinalReportLabel(ProductFailure))
	}
}

func TestPassLabel(t *testing.T) {
	if FinalReportLabel(Pass) != "FULL PILOT DEBUG-QUALIFIED — EXTERNAL OAUTH/SOAK REMAIN" {
		t.Fatal(FinalReportLabel(Pass))
	}
}
