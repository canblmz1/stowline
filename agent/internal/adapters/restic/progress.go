package restic

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

var textPercentRE = regexp.MustCompile(`\]\s+([0-9]+(?:\.[0-9]+)?)%`)

// textStatusRE matches restic's text status line, e.g.
// "[0:01] 42.73%  1 files 88.889 MiB, total 7 files 208.002 MiB, 0 errors ETA 0:01"
// (the first line of a run has no percentage yet).
var textStatusRE = regexp.MustCompile(`^\[[0-9:]+\]\s+(?:([0-9]+(?:\.[0-9]+)?)%\s+)?([0-9]+) files ([0-9]+(?:\.[0-9]+)?) (B|KiB|MiB|GiB|TiB), total ([0-9]+) files ([0-9]+(?:\.[0-9]+)?) (B|KiB|MiB|GiB|TiB)`)

var textUnits = map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}

func textBytes(num, unit string) int64 {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return int64(v * textUnits[unit])
}

type backupProgressParser struct {
	mu       sync.Mutex
	tail     string
	report   func(domain.BackupProgress)
	last     float64
	reported bool
	// onActivity runs whenever any parsed number changes (not just the
	// percentage); the adapter's stall watchdog uses it.
	onActivity func()
	lastSeen   domain.BackupProgress
	seen       bool
}

func newBackupProgressParser(report func(domain.BackupProgress)) *backupProgressParser {
	return &backupProgressParser{report: report}
}

// Feed accepts raw restic output locally, but emits numbers only. VSS-required
// backups use restic's text printer (to retain positive VSS evidence), while
// non-VSS runs use JSON status lines; both formats are handled here.
func (p *backupProgressParser) Feed(chunk []byte) {
	if p == nil || (p.report == nil && p.onActivity == nil) || len(chunk) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.tail += string(chunk)
	parts := strings.FieldsFunc(p.tail, func(r rune) bool { return r == '\r' || r == '\n' })
	complete := strings.HasSuffix(p.tail, "\r") || strings.HasSuffix(p.tail, "\n")
	p.tail = ""
	if !complete && len(parts) > 0 {
		p.tail = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	if len(p.tail) > 4096 {
		p.tail = p.tail[len(p.tail)-4096:]
	}
	for _, line := range parts {
		p.parseLine(strings.TrimSpace(line))
	}
}

func (p *backupProgressParser) parseLine(line string) {
	var update domain.BackupProgress
	var found bool
	if strings.HasPrefix(line, "{") {
		var status struct {
			MessageType string  `json:"message_type"`
			PercentDone float64 `json:"percent_done"`
			FilesDone   int64   `json:"files_done"`
			TotalFiles  int64   `json:"total_files"`
			BytesDone   int64   `json:"bytes_done"`
			TotalBytes  int64   `json:"total_bytes"`
		}
		if json.Unmarshal([]byte(line), &status) == nil && status.MessageType == "status" {
			update = domain.BackupProgress{
				PercentDone: status.PercentDone * 100,
				FilesDone:   status.FilesDone, TotalFiles: status.TotalFiles,
				BytesDone: status.BytesDone, TotalBytes: status.TotalBytes,
			}
			found = true
		}
	} else if m := textStatusRE.FindStringSubmatch(line); len(m) == 8 {
		if m[1] != "" {
			update.PercentDone, _ = strconv.ParseFloat(m[1], 64)
		}
		update.FilesDone, _ = strconv.ParseInt(m[2], 10, 64)
		update.BytesDone = textBytes(m[3], m[4])
		update.TotalFiles, _ = strconv.ParseInt(m[5], 10, 64)
		update.TotalBytes = textBytes(m[6], m[7])
		if m[1] == "" && update.TotalBytes > 0 {
			// restic omits the percentage on its first line and once every
			// byte is read; derive it so the rings never drop to 0%.
			update.PercentDone = float64(update.BytesDone) * 100 / float64(update.TotalBytes)
		}
		found = true
	} else if match := textPercentRE.FindStringSubmatch(line); len(match) == 2 {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			update.PercentDone = value
			found = true
		}
	}
	if !found {
		return
	}
	if update.PercentDone < 0 {
		update.PercentDone = 0
	} else if update.PercentDone > 100 {
		update.PercentDone = 100
	}
	if !p.seen || update != p.lastSeen {
		p.lastSeen, p.seen = update, true
		if p.onActivity != nil {
			p.onActivity()
		}
	}
	if p.report == nil || (p.reported && update.PercentDone == p.last) {
		return
	}
	p.last, p.reported = update.PercentDone, true
	p.report(update)
}

// restoreProgressParser turns `restic restore --json` status and summary lines
// into numbers. Verbose lines carry file names and are ignored on purpose:
// only counts ever leave the adapter.
type restoreProgressParser struct {
	mu     sync.Mutex
	tail   string
	report func(domain.RestoreProgress)
}

func newRestoreProgressParser(report func(domain.RestoreProgress)) *restoreProgressParser {
	return &restoreProgressParser{report: report}
}

func (p *restoreProgressParser) Feed(chunk []byte) {
	if p == nil || p.report == nil || len(chunk) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tail += string(chunk)
	lines := strings.Split(p.tail, "\n")
	p.tail = lines[len(lines)-1]
	if len(p.tail) > 4096 {
		p.tail = p.tail[len(p.tail)-4096:]
	}
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var status struct {
			MessageType   string  `json:"message_type"`
			PercentDone   float64 `json:"percent_done"`
			TotalFiles    int64   `json:"total_files"`
			FilesRestored int64   `json:"files_restored"`
			TotalBytes    int64   `json:"total_bytes"`
			BytesRestored int64   `json:"bytes_restored"`
		}
		if json.Unmarshal([]byte(line), &status) != nil || (status.MessageType != "status" && status.MessageType != "summary") {
			continue
		}
		pct := status.PercentDone * 100
		if status.MessageType == "summary" {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		p.report(domain.RestoreProgress{
			PercentDone: pct,
			FilesDone:   status.FilesRestored, TotalFiles: status.TotalFiles,
			BytesDone: status.BytesRestored, TotalBytes: status.TotalBytes,
		})
	}
}
