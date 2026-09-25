package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultPilotPolicyIsIstanbulAfternoonWindow(t *testing.T) {
	p := DefaultPilotPolicy([]string{`C:\x`})
	if p.Schedule.Timezone != "Europe/Istanbul" {
		t.Fatalf("timezone=%s", p.Schedule.Timezone)
	}
	if p.Schedule.WindowStartHHMM != "12:00" || p.Schedule.WindowEndHHMM != "16:30" {
		t.Fatalf("window=%s-%s", p.Schedule.WindowStartHHMM, p.Schedule.WindowEndHHMM)
	}
	if p.Schedule.EligibilityStartHHMM != "08:30" || p.Schedule.HardStopHHMM != "17:45" {
		t.Fatalf("eligibility/hardstop=%s/%s", p.Schedule.EligibilityStartHHMM, p.Schedule.HardStopHHMM)
	}
	if p.BandwidthKiBps != 0 || !p.LabLocalUnlimited {
		t.Fatalf("lab default must not silently use 2048 KiB/s: kibps=%d unlimited=%v", p.BandwidthKiBps, p.LabLocalUnlimited)
	}
}

func TestLocalPolicyRejectsWANUnlimited(t *testing.T) {
	p := DefaultPilotPolicy([]string{`C:\x`})
	if err := p.ValidateForBackend(BackendLocal); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateForBackend(BackendRESTGateway); err == nil {
		t.Fatal("REST must refuse LabLocalUnlimited / 0 KiB/s")
	}
	p.LabLocalUnlimited = false
	p.BandwidthKiBps = 439
	if err := p.ValidateForBackend(BackendRESTGateway); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulePolicyValidate(t *testing.T) {
	ok := DefaultPilotPolicy([]string{`C:\x`}).Schedule
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	badTZ := ok
	badTZ.Timezone = "Not/AZone"
	if err := badTZ.Validate(); err == nil || !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("tz: %v", err)
	}
	badHH := ok
	badHH.WindowStartHHMM = "9:00"
	if err := badHH.Validate(); err == nil {
		t.Fatal("HH:MM must be zero-padded")
	}
	overnight := ok
	overnight.WindowStartHHMM = "22:00"
	overnight.WindowEndHHMM = "06:00"
	if err := overnight.Validate(); err == nil || !strings.Contains(err.Error(), "overnight") {
		t.Fatalf("overnight: %v", err)
	}
	equal := ok
	equal.WindowEndHHMM = equal.WindowStartHHMM
	if err := equal.Validate(); err == nil {
		t.Fatal("end == start must fail")
	}
}

func TestRestoreIdentityHashStableAndSecretFree(t *testing.T) {
	snap := SnapshotID(strings.Repeat("a", 64))
	r := RestoreRequest{
		SnapshotID:  snap,
		Selections:  []string{"b", "a"},
		StagingRoot: `C:\Stowline\Restore`,
		Repository:  RepositoryDescriptor{BackendKind: BackendLocal, Location: `C:\r`, GenerationID: "g", DeviceID: "d"},
		PasswordRef: SecretRef{Locator: "must-not-appear", Provider: SecretProviderFile},
	}
	h1, payload, err := RestoreIdentityHash(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Selections = []string{"a", "b"}
	h2, _, err := RestoreIdentityHash(r)
	if err != nil || h1 != h2 {
		t.Fatalf("selection order must not change hash: %s %s %v", h1, h2, err)
	}
	if strings.Contains(payload, "must-not-appear") || strings.Contains(payload, "password") {
		t.Fatalf("secret leaked into hash payload: %s", payload)
	}
	r.SnapshotID = SnapshotID(strings.Repeat("b", 64))
	h3, _, _ := RestoreIdentityHash(r)
	if h3 == h1 {
		t.Fatal("changed snapshot must change hash")
	}
}
