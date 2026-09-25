package domain

import "testing"

// Regression: a length-mismatched slice comparison (slot[:12] against the
// 8-byte literal "control|") used to be always-false, silently
// misclassifying every operator-triggered "Backup Now" as a site-wide
// seed-admission job for any device with no prior successful backup.
// ControlSlotKey/IsControlSlotKey exist so that class of bug -- comparing a
// prefix by hand-picked slice length instead of strings.HasPrefix -- cannot
// happen again here.
func TestControlSlotKeyRoundTrips(t *testing.T) {
	for _, deviceID := range []string{
		"6cfa98b3-d22b-4de7-952f-015f2ccd5cbc",
		"a",
		"",
		"315e02c1-7a08-4411-8dbc-ad24c1070151-with-a-much-longer-suffix",
	} {
		slot := ControlSlotKey(deviceID)
		if !IsControlSlotKey(slot) {
			t.Fatalf("ControlSlotKey(%q) = %q must satisfy IsControlSlotKey", deviceID, slot)
		}
	}
}

func TestIsControlSlotKeyRejectsNonControlSlots(t *testing.T) {
	for _, slot := range []string{"", "qualification|abc123", "scheduled|xyz", "control", "contro|short"} {
		if IsControlSlotKey(slot) {
			t.Fatalf("IsControlSlotKey(%q) must be false", slot)
		}
	}
}
