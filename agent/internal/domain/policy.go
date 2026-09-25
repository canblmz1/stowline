package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"
)

// LocalPolicy is the last accepted endpoint policy.
// WAN backups still require a live admission lease; cached policy is not permission to start.
type LocalPolicy struct {
	RevisionID           string
	SchemaVersion        int
	ContentHash          string
	AcceptedAt           time.Time
	IssuedAt             time.Time
	EffectiveAt          time.Time
	SourceRoots          []string
	OptionalRoots        []string
	Excludes             []string
	VSSMode              VSSMode
	Schedule             SchedulePolicy
	BandwidthKiBps       int
	RestoreDownloadKiBps int
	// LabLocalUnlimited permits BandwidthKiBps==0 only for LOCAL/PILOT_RCLONE_LOCAL.
	LabLocalUnlimited   bool
	JobDeadline         time.Duration
	CacheBudgetMiB      int
	FreeSpaceFloorBytes int64
	SiteID              string
}

type SchedulePolicy struct {
	Epoch                string
	EligibilityStartHHMM string
	WindowStartHHMM      string
	WindowEndHHMM        string // START DEADLINE for new jobs
	HardStopHHMM         string
	Timezone             string
	CatchUp              bool
	JitterMaxMinutes     int
	MaxAttempts          int
	RetryDelay1          time.Duration
	RetryDelay2          time.Duration
	BootDelayMin         time.Duration
	BootDelayMax         time.Duration
	NormalMaxRuntime     time.Duration
	SeedMaxRuntime       time.Duration
}

func (p LocalPolicy) Validate() error {
	if err := p.Schedule.Validate(); err != nil {
		return err
	}
	if p.BandwidthKiBps < 0 || p.RestoreDownloadKiBps < 0 {
		return fmt.Errorf("%w: bandwidth must not be negative", ErrPolicyInvalid)
	}
	if p.BandwidthKiBps > MaxResticKiBps || p.RestoreDownloadKiBps > MaxResticKiBps {
		return fmt.Errorf("%w: bandwidth overflow", ErrPolicyInvalid)
	}
	return nil
}

func (p LocalPolicy) ValidateForBackend(kind BackendKind) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if RequiresWANRateLimit(kind) {
		if p.LabLocalUnlimited {
			return fmt.Errorf("%w: LabLocalUnlimited is forbidden on WAN backends", ErrPolicyInvalid)
		}
		return ValidateResticKiBps(p.BandwidthKiBps)
	}
	if p.BandwidthKiBps == 0 && !p.LabLocalUnlimited {
		return fmt.Errorf("%w: local unlimited upload requires LabLocalUnlimited", ErrPolicyInvalid)
	}
	if p.BandwidthKiBps > 0 {
		return ValidateResticKiBps(p.BandwidthKiBps)
	}
	return nil
}

func (s SchedulePolicy) Validate() error {
	if s.Timezone == "" {
		return fmt.Errorf("%w: timezone required", ErrPolicyInvalid)
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("%w: timezone %q: %v", ErrPolicyInvalid, s.Timezone, err)
	}
	sh, sm, err := ParseHHMM(s.WindowStartHHMM)
	if err != nil {
		return fmt.Errorf("%w: window start: %v", ErrPolicyInvalid, err)
	}
	eh, em, err := ParseHHMM(s.WindowEndHHMM)
	if err != nil {
		return fmt.Errorf("%w: window end: %v", ErrPolicyInvalid, err)
	}
	start := sh*60 + sm
	end := eh*60 + em
	if end <= start {
		return fmt.Errorf("%w: overnight windows are not implemented; WindowEnd must be after WindowStart", ErrPolicyInvalid)
	}
	if s.EligibilityStartHHMM != "" {
		xh, xm, err := ParseHHMM(s.EligibilityStartHHMM)
		if err != nil {
			return fmt.Errorf("%w: eligibility start: %v", ErrPolicyInvalid, err)
		}
		if xh*60+xm > start {
			return fmt.Errorf("%w: eligibility start must be at or before window start", ErrPolicyInvalid)
		}
	}
	if s.HardStopHHMM != "" {
		hh, hm, err := ParseHHMM(s.HardStopHHMM)
		if err != nil {
			return fmt.Errorf("%w: hard stop: %v", ErrPolicyInvalid, err)
		}
		if hh*60+hm < end {
			return fmt.Errorf("%w: hard stop must be at or after start deadline", ErrPolicyInvalid)
		}
	}
	if s.JitterMaxMinutes < 0 {
		return fmt.Errorf("%w: jitter must not be negative", ErrPolicyInvalid)
	}
	if s.MaxAttempts < 0 {
		return fmt.Errorf("%w: max attempts must not be negative", ErrPolicyInvalid)
	}
	return nil
}

func (s SchedulePolicy) MaxAttemptsOrDefault() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return 3
}

func (s SchedulePolicy) RetryAfter(failedAttemptNo int) time.Duration {
	switch failedAttemptNo {
	case 1:
		if s.RetryDelay1 > 0 {
			return s.RetryDelay1
		}
		return 15 * time.Minute
	case 2:
		if s.RetryDelay2 > 0 {
			return s.RetryDelay2
		}
		return 60 * time.Minute
	default:
		return time.Hour
	}
}

func (s SchedulePolicy) RuntimeForClass(class WANClass) time.Duration {
	if class == WANClassInitialSeed {
		if s.SeedMaxRuntime > 0 {
			return s.SeedMaxRuntime
		}
		return 8 * time.Hour
	}
	if s.NormalMaxRuntime > 0 {
		return s.NormalMaxRuntime
	}
	return 4 * time.Hour
}

func ParseHHMM(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("HH:MM required")
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour")
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute")
	}
	return hour, minute, nil
}

func DefaultPilotPolicy(roots []string) LocalPolicy {
	return LocalPolicy{
		RevisionID:    "pilot-local-1",
		SchemaVersion: 1,
		ContentHash:   "pilot",
		AcceptedAt:    time.Now().UTC(),
		SourceRoots:   roots,
		VSSMode:       VSSRequired,
		Schedule: SchedulePolicy{
			Epoch:                "pilot",
			EligibilityStartHHMM: "08:30",
			WindowStartHHMM:      "12:00",
			WindowEndHHMM:        "16:30",
			HardStopHHMM:         "17:45",
			Timezone:             "Europe/Istanbul",
			CatchUp:              true,
			JitterMaxMinutes:     15,
			MaxAttempts:          3,
			RetryDelay1:          15 * time.Minute,
			RetryDelay2:          60 * time.Minute,
			BootDelayMin:         5 * time.Minute,
			BootDelayMax:         20 * time.Minute,
			NormalMaxRuntime:     4 * time.Hour,
			SeedMaxRuntime:       8 * time.Hour,
		},
		BandwidthKiBps:      0,
		LabLocalUnlimited:   true,
		JobDeadline:         4 * time.Hour,
		CacheBudgetMiB:      2048,
		FreeSpaceFloorBytes: 2 * 1024 * 1024 * 1024,
	}
}
