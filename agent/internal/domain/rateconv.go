package domain

import (
	"fmt"
	"math"
)

// Rate conversion uses SI megabits for operator-facing WAN capacity and
// kibibytes/second (1024-byte KiB) for restic --limit-upload/--limit-download.
//
// 1 Mbps (SI) = 1_000_000 bit/s = 125_000 B/s ≈ 122.0703125 KiB/s.
const (
	SIMegabitBitsPerSecond = 1_000_000
	KiBBytes               = 1024
	BitsPerByte            = 8
	// AgentSafetyMargin withholds 10% of the site application budget so TLS/HTTP
	// stay under the gateway aggregate cap.
	AgentSafetyMargin = 0.90
	// MaxResticKiBps rejects absurd compiled ceilings (≈800 Mbps application).
	MaxResticKiBps = 100_000
	// QualificationSiteMbps / QualificationBudgetPercent compile the synthetic
	// Workspace CLI ceiling (Branch proof table). Scheduled WAN must not
	// use these; it requires a measured site and an admission lease.
	QualificationSiteMbps      = 20
	QualificationBudgetPercent = 20
)

// SiteBudgetBPS is measured SI Mbps times a business-hour percentage, in bit/s.
func SiteBudgetBPS(measuredUploadMbps float64, budgetPercent int) (int64, error) {
	if measuredUploadMbps <= 0 || math.IsNaN(measuredUploadMbps) || math.IsInf(measuredUploadMbps, 0) {
		return 0, fmt.Errorf("%w: measured_upload_mbps must be a positive finite value", ErrPolicyInvalid)
	}
	if budgetPercent < 5 || budgetPercent > 50 {
		return 0, fmt.Errorf("%w: business_backup_budget_percent must be 5-50", ErrPolicyInvalid)
	}
	bps := measuredUploadMbps * SIMegabitBitsPerSecond * float64(budgetPercent) / 100
	if bps > float64(math.MaxInt64) {
		return 0, fmt.Errorf("%w: site budget overflow", ErrPolicyInvalid)
	}
	return int64(math.Round(bps)), nil
}

// BytesPerSecondFromBPS converts bit/s to integer bytes/s (floor).
func BytesPerSecondFromBPS(bps int64) (int64, error) {
	if bps <= 0 {
		return 0, fmt.Errorf("%w: bit rate must be positive", ErrPolicyInvalid)
	}
	return bps / BitsPerByte, nil
}

// ResticKiBpsFromBytesPerSecond is the exact restic --limit-upload unit.
func ResticKiBpsFromBytesPerSecond(bytesPerSec int64) (float64, error) {
	if bytesPerSec <= 0 {
		return 0, fmt.Errorf("%w: byte rate must be positive", ErrPolicyInvalid)
	}
	return float64(bytesPerSec) / float64(KiBBytes), nil
}

// AgentCeilingKiBps applies the 10% safety margin and fair-share split, then floors.
// activeJobs must be >= 1 (the job being admitted).
func AgentCeilingKiBps(siteBudgetBPS int64, activeJobs int) (int, error) {
	if activeJobs < 1 {
		return 0, fmt.Errorf("%w: activeJobs must be >= 1", ErrPolicyInvalid)
	}
	bytesPerSec, err := BytesPerSecondFromBPS(siteBudgetBPS)
	if err != nil {
		return 0, err
	}
	rawKiBps, err := ResticKiBpsFromBytesPerSecond(bytesPerSec)
	if err != nil {
		return 0, err
	}
	share := rawKiBps * AgentSafetyMargin / float64(activeJobs)
	if share > float64(MaxResticKiBps) {
		return 0, fmt.Errorf("%w: compiled KiB/s overflow", ErrPolicyInvalid)
	}
	out := int(math.Floor(share))
	if out < 1 {
		return 0, fmt.Errorf("%w: compiled agent ceiling is below 1 KiB/s", ErrPolicyInvalid)
	}
	return out, nil
}

// ValidateResticKiBps rejects 0/negative/overflow for WAN (REST/Drive) use.
// Zero is not unlimited.
func ValidateResticKiBps(kibps int) error {
	if kibps < 0 {
		return fmt.Errorf("%w: bandwidth must not be negative", ErrPolicyInvalid)
	}
	if kibps == 0 {
		return fmt.Errorf("%w: bandwidth 0 is invalid for WAN; unset means do not start", ErrPolicyInvalid)
	}
	if kibps > MaxResticKiBps {
		return fmt.Errorf("%w: bandwidth KiB/s exceeds maximum", ErrPolicyInvalid)
	}
	return nil
}

// RequiresWANRateLimit reports backends that leave the office uplink.
func RequiresWANRateLimit(kind BackendKind) bool {
	switch kind {
	case BackendRESTGateway, BackendPilotRcloneDrive, BackendS3, BackendB2:
		return true
	default:
		return false
	}
}

// QualificationWANCeilingKiBps is the compiled QualAgent CLI ceiling: 20 Mbps × 20% × 0.90 → 439.
func QualificationWANCeilingKiBps() (int, error) {
	bps, err := SiteBudgetBPS(QualificationSiteMbps, QualificationBudgetPercent)
	if err != nil {
		return 0, err
	}
	return AgentCeilingKiBps(bps, 1)
}

// CLIWANLimits applies restic limits for operator CLI. LOCAL may omit limits.
// REST/Drive CLI is allowed only for QualificationOnly repositories and always
// carries the compiled ceiling. Scheduled WAN still requires a control-plane lease.
func CLIWANLimits(kind BackendKind, qualificationOnly bool) (upload, download int, err error) {
	if !RequiresWANRateLimit(kind) {
		return 0, 0, nil
	}
	if !qualificationOnly {
		return 0, 0, fmt.Errorf("%w: WAN CLI requires QualificationOnly; scheduled WAN uses admission leases", ErrConfig)
	}
	kib, err := QualificationWANCeilingKiBps()
	if err != nil {
		return 0, 0, err
	}
	return kib, kib, nil
}
