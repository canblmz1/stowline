package domain

import (
	"math"
	"testing"
)

func TestFourMbpsSiteBudgetToResticKiBps(t *testing.T) {
	bps, err := SiteBudgetBPS(20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if bps != 4_000_000 {
		t.Fatalf("bps=%d want 4000000", bps)
	}
	bytesPerSec, err := BytesPerSecondFromBPS(bps)
	if err != nil {
		t.Fatal(err)
	}
	if bytesPerSec != 500_000 {
		t.Fatalf("B/s=%d want 500000", bytesPerSec)
	}
	kib, err := ResticKiBpsFromBytesPerSecond(bytesPerSec)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(kib-488.28125) > 1e-9 {
		t.Fatalf("KiB/s=%v want 488.28125", kib)
	}
	agent, err := AgentCeilingKiBps(bps, 1)
	if err != nil {
		t.Fatal(err)
	}
	if agent != 439 {
		t.Fatalf("agent ceiling=%d want 439 (floor(488.28125*0.90))", agent)
	}
	two, err := AgentCeilingKiBps(bps, 2)
	if err != nil {
		t.Fatal(err)
	}
	if two != 219 {
		t.Fatalf("two-job share=%d want 219", two)
	}
}

func TestSiteBudgetTable(t *testing.T) {
	cases := []struct {
		mbps  float64
		bps   int64
		kib   float64
		agent int
	}{
		{5, 1_000_000, 122.0703125, 109},
		{10, 2_000_000, 244.140625, 219},
		{20, 4_000_000, 488.28125, 439},
		{50, 10_000_000, 1220.703125, 1098},
		{100, 20_000_000, 2441.40625, 2197},
		{200, 40_000_000, 4882.8125, 4394},
	}
	for _, tc := range cases {
		bps, err := SiteBudgetBPS(tc.mbps, 20)
		if err != nil || bps != tc.bps {
			t.Fatalf("%v Mbps: bps=%d err=%v", tc.mbps, bps, err)
		}
		kib, err := ResticKiBpsFromBytesPerSecond(bps / 8)
		if err != nil || math.Abs(kib-tc.kib) > 1e-6 {
			t.Fatalf("%v Mbps: kib=%v want %v", tc.mbps, kib, tc.kib)
		}
		agent, err := AgentCeilingKiBps(bps, 1)
		if err != nil || agent != tc.agent {
			t.Fatalf("%v Mbps: agent=%d want %d err=%v", tc.mbps, agent, tc.agent, err)
		}
	}
}

func TestValidateResticKiBpsRejectsZeroNegativeOverflow(t *testing.T) {
	if err := ValidateResticKiBps(0); err == nil {
		t.Fatal("0 must be invalid for WAN")
	}
	if err := ValidateResticKiBps(-1); err == nil {
		t.Fatal("negative must be invalid")
	}
	if err := ValidateResticKiBps(MaxResticKiBps + 1); err == nil {
		t.Fatal("overflow must be invalid")
	}
	if err := ValidateResticKiBps(439); err != nil {
		t.Fatal(err)
	}
}

func TestSiteBudgetRejectsUnsetAndAbsurdPercent(t *testing.T) {
	if _, err := SiteBudgetBPS(0, 20); err == nil {
		t.Fatal("unset/zero measured upload must fail")
	}
	if _, err := SiteBudgetBPS(20, 0); err == nil {
		t.Fatal("0% must fail")
	}
	if _, err := SiteBudgetBPS(20, 100); err == nil {
		t.Fatal("100% must fail closed for v1")
	}
}

func TestRequiresWANRateLimit(t *testing.T) {
	if !RequiresWANRateLimit(BackendRESTGateway) || !RequiresWANRateLimit(BackendPilotRcloneDrive) {
		t.Fatal("REST/Drive must require WAN rate limit")
	}
	if RequiresWANRateLimit(BackendLocal) {
		t.Fatal("LOCAL lab must not be treated as WAN")
	}
}

func TestQualificationWANCeilingIs439(t *testing.T) {
	got, err := QualificationWANCeilingKiBps()
	if err != nil || got != 439 {
		t.Fatalf("got %d err=%v want 439", got, err)
	}
}

func TestCLIWANLimits(t *testing.T) {
	up, down, err := CLIWANLimits(BackendLocal, false)
	if err != nil || up != 0 || down != 0 {
		t.Fatalf("LOCAL CLI must omit limits: %d %d %v", up, down, err)
	}
	if _, _, err := CLIWANLimits(BackendRESTGateway, false); err == nil {
		t.Fatal("non-qualification REST CLI must fail closed")
	}
	up, down, err = CLIWANLimits(BackendRESTGateway, true)
	if err != nil || up != 439 || down != 439 {
		t.Fatalf("qualification REST CLI: %d %d %v", up, down, err)
	}
}
