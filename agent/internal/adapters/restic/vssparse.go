package restic

import (
	"bufio"
	"regexp"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// Version-bound to Restic 0.19.1 internal/fs/fs_local_vss.go messages:
//
//	creating VSS snapshot for [%s]
//	successfully created snapshot for [%s]
//	failed to create snapshot for [%s]: %s
//	snapshots for [%s] excluded by user
//
// Do not infer success from exit 0. Live-path fallback after failure is treated as CONSISTENCY_NOT_MET
// when vss_mode=required.
var (
	reVSSCreating  = regexp.MustCompile(`(?i)creating VSS snapshot for \[(.+?)\]`)
	reVSSSuccess   = regexp.MustCompile(`(?i)successfully created snapshot for \[(.+?)\]`)
	reVSSFailed    = regexp.MustCompile(`(?i)failed to create snapshot for \[(.+?)\]:\s*(.+)`)
	reVSSExcluded  = regexp.MustCompile(`(?i)snapshots for \[(.+?)\] excluded by user`)
	reVSSPrivilege = regexp.MustCompile(`(?i)VSS error:.*(?:insufficient backup privileges|not an administrator|E_ACCESSDENIED)`)
)

func parseVSSEvidenceFromProcess(stdout, stderr []byte, truncated bool, mode domain.VSSMode, requiredVolumes []string, engineVersion string) domain.VSSEvidence {
	ev := parseVSSEvidence(string(engineOutput(stdout, stderr)), mode, requiredVolumes, engineVersion)
	if truncated {
		ev.ParserNotes = append(ev.ParserNotes, "captured restic stdout/stderr was truncated; refusing CONSISTENCY_VERIFIED")
		ev.PositiveAllRequired = false
	}
	return ev
}

func parseVSSEvidence(text string, mode domain.VSSMode, requiredVolumes []string, engineVersion string) domain.VSSEvidence {
	ev := domain.VSSEvidence{
		EngineVersion: engineVersion,
		ParserBoundTo: VSSParserBoundTo,
		Mode:          mode,
	}
	if engineVersion != "" && !strings.Contains(engineVersion, PinnedVersion) {
		ev.ParserNotes = append(ev.ParserNotes, "engine version is not the pinned 0.19.1 baseline; parser still applied fail-closed")
	}
	byVol := map[string]*domain.VolumeEvidence{}
	touch := func(vol string) *domain.VolumeEvidence {
		key := normalizeVolumeKey(vol)
		if v, ok := byVol[key]; ok {
			return v
		}
		nv := &domain.VolumeEvidence{Volume: strings.TrimSpace(vol)}
		byVol[key] = nv
		return nv
	}

	privilegeSeen := false
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if m := reVSSCreating.FindStringSubmatch(line); m != nil {
			touch(m[1]).CreatingSeen = true
			ev.RawMatchedLines = append(ev.RawMatchedLines, domain.Redact(line))
			continue
		}
		if m := reVSSSuccess.FindStringSubmatch(line); m != nil {
			v := touch(m[1])
			v.SuccessSeen = true
			ev.RawMatchedLines = append(ev.RawMatchedLines, domain.Redact(line))
			continue
		}
		if m := reVSSFailed.FindStringSubmatch(line); m != nil {
			v := touch(m[1])
			v.FailureSeen = true
			v.FailureMessage = sanitizeDiagnostic(m[2])
			v.LiveFallbackLikely = true
			ev.RawMatchedLines = append(ev.RawMatchedLines, domain.Redact(line))
			continue
		}
		if reVSSPrivilege.MatchString(line) {
			privilegeSeen = true
			ev.ParserNotes = append(ev.ParserNotes, sanitizeDiagnostic(line))
			ev.RawMatchedLines = append(ev.RawMatchedLines, domain.Redact(line))
			continue
		}
		if m := reVSSExcluded.FindStringSubmatch(line); m != nil {
			v := touch(m[1])
			v.ExcludedByUser = true
			ev.RawMatchedLines = append(ev.RawMatchedLines, domain.Redact(line))
		}
	}
	if err := sc.Err(); err != nil {
		ev.ParserNotes = append(ev.ParserNotes, "vss parser scan error; refusing CONSISTENCY_VERIFIED")
		ev.PositiveAllRequired = false
		return ev
	}

	for _, v := range byVol {
		ev.Volumes = append(ev.Volumes, *v)
	}

	if mode == domain.VSSDisabled {
		ev.PositiveAllRequired = false
		return ev
	}

	positive := true
	if len(requiredVolumes) == 0 {
		// Require at least one successful snapshot message for required mode.
		found := false
		for _, v := range ev.Volumes {
			if v.SuccessSeen && !v.FailureSeen {
				found = true
			}
			if v.FailureSeen || v.LiveFallbackLikely {
				positive = false
			}
		}
		positive = positive && found
	} else {
		for _, req := range requiredVolumes {
			match := findVolume(ev.Volumes, req)
			if match == nil || !match.SuccessSeen || match.FailureSeen {
				positive = false
			}
		}
	}
	if privilegeSeen {
		positive = false
	}
	ev.PositiveAllRequired = positive
	return ev
}

func findVolume(vols []domain.VolumeEvidence, want string) *domain.VolumeEvidence {
	w := normalizeVolumeKey(want)
	for i := range vols {
		v := normalizeVolumeKey(vols[i].Volume)
		if v == w || strings.HasPrefix(v, w) || strings.HasPrefix(w, v) {
			return &vols[i]
		}
	}
	return nil
}

func normalizeVolumeKey(vol string) string {
	s := strings.ToLower(strings.TrimSpace(vol))
	s = strings.TrimRight(s, `\`)
	s = strings.TrimRight(s, `/`)
	return s
}

func volumesForRoots(roots []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range roots {
		if len(r) >= 2 && r[1] == ':' {
			vol := strings.ToLower(r[:2]) + `\`
			if _, ok := seen[vol]; ok {
				continue
			}
			seen[vol] = struct{}{}
			out = append(out, vol)
		}
	}
	return out
}
