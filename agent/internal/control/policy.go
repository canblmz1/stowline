package control

import (
	"fmt"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func asString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, x := range raw {
		s, _ := x.(string)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func asDurationMinutes(v any) time.Duration {
	n := asInt(v)
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Minute
}

// PolicyFromServer maps control-plane JSON onto LocalPolicy. Malformed input fails closed.
func PolicyFromServer(revisionID string, cfg map[string]any) (domain.LocalPolicy, error) {
	if cfg == nil {
		return domain.LocalPolicy{}, fmt.Errorf("%w: empty policy", domain.ErrPolicyInvalid)
	}
	p := domain.LocalPolicy{
		RevisionID:    revisionID,
		SchemaVersion: asInt(cfg["schema_version"]),
		AcceptedAt:    time.Now().UTC(),
		SiteID:        asString(cfg["site_id"]),
	}
	if p.SchemaVersion == 0 {
		p.SchemaVersion = 1
	}
	p.SourceRoots = asStringSlice(cfg["source_roots"])
	p.OptionalRoots = asStringSlice(cfg["optional_roots"])
	p.Excludes = asStringSlice(cfg["excludes"])
	switch asString(cfg["vss_mode"]) {
	case "required":
		p.VSSMode = domain.VSSRequired
	case "disabled":
		p.VSSMode = domain.VSSDisabled
	case "":
		p.VSSMode = domain.VSSRequired
	default:
		return domain.LocalPolicy{}, fmt.Errorf("%w: vss_mode", domain.ErrPolicyInvalid)
	}
	if sched, ok := cfg["schedule"].(map[string]any); ok {
		p.Schedule = domain.SchedulePolicy{
			Epoch:                asString(sched["epoch"]),
			Timezone:             asString(sched["timezone"]),
			EligibilityStartHHMM: asString(sched["eligibility_start"]),
			WindowStartHHMM:      asString(sched["window_start"]),
			WindowEndHHMM:        asString(sched["window_end"]),
			HardStopHHMM:         asString(sched["hard_stop"]),
			CatchUp:              asBool(sched["catch_up"]),
			JitterMaxMinutes:     asInt(sched["jitter_max_minutes"]),
			MaxAttempts:          asInt(sched["max_attempts"]),
			RetryDelay1:          asDurationMinutes(sched["retry_delay_1_minutes"]),
			RetryDelay2:          asDurationMinutes(sched["retry_delay_2_minutes"]),
			BootDelayMin:         asDurationMinutes(sched["boot_delay_min_minutes"]),
			BootDelayMax:         asDurationMinutes(sched["boot_delay_max_minutes"]),
			NormalMaxRuntime:     asDurationMinutes(sched["normal_max_runtime_minutes"]),
			SeedMaxRuntime:       asDurationMinutes(sched["seed_max_runtime_minutes"]),
		}
	}
	if bw, ok := cfg["bandwidth"].(map[string]any); ok {
		p.BandwidthKiBps = asInt(bw["upload_limit_kibps"])
		p.RestoreDownloadKiBps = asInt(bw["restore_download_limit_kibps"])
		p.LabLocalUnlimited = asBool(bw["lab_local_unlimited"])
	}
	if p.Schedule.Timezone == "" {
		return domain.LocalPolicy{}, fmt.Errorf("%w: timezone required", domain.ErrPolicyInvalid)
	}
	if err := p.Validate(); err != nil {
		return domain.LocalPolicy{}, err
	}
	return p, nil
}
