package restic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// restic 0.19.1 internal/ui/backup/text.go Finish(): printer.P("snapshot %s saved\n", id.Str())
// id.Str() is the 8-character hex prefix, not the full 64-character ID.
var reSnapshotSaved = regexp.MustCompile(`(?im)^snapshot ([0-9a-f]{8,64}) saved\s*$`)

type jsonLine struct {
	MessageType string          `json:"message_type"`
	SnapshotID  string          `json:"snapshot_id"`
	ID          string          `json:"id"`
	Code        int             `json:"code"`
	Message     string          `json:"message"`
	Raw         json.RawMessage `json:"-"`
}

type backupSummaryJSON struct {
	MessageType         string `json:"message_type"`
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	FilesUnmodified     int64  `json:"files_unmodified"`
	DirsNew             int64  `json:"dirs_new"`
	DirsChanged         int64  `json:"dirs_changed"`
	DirsUnmodified      int64  `json:"dirs_unmodified"`
	DataAdded           int64  `json:"data_added"`
	DataAddedPacked     int64  `json:"data_added_packed"`
	TotalFilesProcessed int64  `json:"total_files_processed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	SnapshotID          string `json:"snapshot_id"`
}

type snapshotJSON struct {
	ID       string   `json:"id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags"`
	Parent   string   `json:"parent"`
}

type repoConfigJSON struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type exitErrorJSON struct {
	MessageType string `json:"message_type"`
	Code        int    `json:"code"`
	Message     string `json:"message"`
}

func parseJSONLines(data []byte) []json.RawMessage {
	var out []json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if json.Valid(line) {
			cp := make([]byte, len(line))
			copy(cp, line)
			out = append(out, cp)
		}
	}
	return out
}

func parseBackupSummary(data []byte) (domain.BackupSummary, error) {
	var sum domain.BackupSummary
	for _, raw := range parseJSONLines(data) {
		var head struct {
			MessageType string `json:"message_type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		if head.MessageType != "summary" {
			continue
		}
		var s backupSummaryJSON
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		sum = domain.BackupSummary{
			FilesNew:            s.FilesNew,
			FilesChanged:        s.FilesChanged,
			FilesUnmodified:     s.FilesUnmodified,
			DirsNew:             s.DirsNew,
			DirsChanged:         s.DirsChanged,
			DirsUnmodified:      s.DirsUnmodified,
			DataAdded:           s.DataAdded,
			DataAddedPacked:     s.DataAddedPacked,
			TotalFilesProcessed: s.TotalFilesProcessed,
			TotalBytesProcessed: s.TotalBytesProcessed,
			SnapshotID:          s.SnapshotID,
			MessageSeen:         true,
		}
	}
	if sum.MessageSeen {
		return sum, nil
	}
	if m := reSnapshotSaved.FindSubmatch(data); m != nil {
		sum.MessageSeen = true
		sum.SnapshotID = string(m[1])
	}
	return sum, nil
}

func engineOutput(stdout, stderr []byte) []byte {
	switch {
	case len(stderr) == 0:
		return stdout
	case len(stdout) == 0:
		return stderr
	default:
		out := make([]byte, 0, len(stdout)+len(stderr)+1)
		out = append(out, stderr...)
		out = append(out, '\n')
		out = append(out, stdout...)
		return out
	}
}

func parseSnapshots(stdout []byte) ([]ports.Snapshot, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []snapshotJSON
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, err
		}
		return mapSnapshots(list), nil
	}
	var out []ports.Snapshot
	for _, raw := range parseJSONLines(stdout) {
		var s snapshotJSON
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if s.ID == "" {
			continue
		}
		out = append(out, mapSnapshot(s))
	}
	return out, nil
}

func mapSnapshots(list []snapshotJSON) []ports.Snapshot {
	out := make([]ports.Snapshot, 0, len(list))
	for _, s := range list {
		out = append(out, mapSnapshot(s))
	}
	return out
}

func mapSnapshot(s snapshotJSON) ports.Snapshot {
	return ports.Snapshot{
		ID:       domain.SnapshotID(s.ID),
		Time:     s.Time,
		Hostname: s.Hostname,
		Paths:    s.Paths,
		Tags:     s.Tags,
		Parent:   s.Parent,
	}
}

func parseRepoConfig(stdout []byte) (string, error) {
	var cfg repoConfigJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &cfg); err != nil {
		return "", err
	}
	return cfg.ID, nil
}

func parseExitError(stderr []byte) *exitErrorJSON {
	for _, raw := range parseJSONLines(stderr) {
		var e exitErrorJSON
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		if e.MessageType == "exit_error" {
			return &e
		}
	}
	return nil
}

type lsLineJSON struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	MTime string `json:"mtime"`
}

// normalizeLsPrefix makes "", "/" and a trailing-slash path all compare
// equal, so callers can pass whatever a client sent without pre-cleaning it.
func normalizeLsPrefix(p string) string {
	if p == "/" {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// lsParentDir returns the directory one level above p ("" for a top-level
// root like "/C"), using restic's own forward-slash path convention (restic
// represents a Windows source root as a leading single-letter segment, e.g.
// "/C/Users/...", regardless of the host OS's own separator).
func lsParentDir(p string) string {
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// parseLsEntries turns raw `restic ls --json <snapshot> [prefix]` output
// into exactly the directory level directly below prefix. Confirmed against
// the pinned restic 0.19.1 binary: `ls` with no path argument does not stop
// at the snapshot's own top-level roots -- it recursively emits every
// ancestor directory node from the filesystem root all the way down through
// every backed-up file, and scoping to a path argument only narrows that
// same full recursion to the given subtree. This function is what actually
// makes the listing "one level at a time": it keeps only the requested
// directory's direct children and drops both that directory's own node and
// anything deeper. Only name/path/type/size/mtime ever survive into the
// returned entries -- restic's own uid/gid/mode/permissions/extended
// attributes and any repository-internal identifiers are dropped here, not
// left for the caller to remember to strip.
func parseLsEntries(data []byte, prefix string) []ports.SnapshotEntry {
	norm := normalizeLsPrefix(prefix)
	var out []ports.SnapshotEntry
	for _, raw := range parseJSONLines(data) {
		var n lsLineJSON
		if err := json.Unmarshal(raw, &n); err != nil {
			continue
		}
		// The leading snapshot-summary line (and any other non-node
		// message) has no name/path pair; a real ls node always does.
		if n.Name == "" || n.Path == "" {
			continue
		}
		p := normalizeLsPrefix(n.Path)
		if p == norm {
			continue // the requested directory's own node, not a child
		}
		if lsParentDir(n.Path) != norm {
			continue // deeper than one level below prefix
		}
		out = append(out, ports.SnapshotEntry{Name: n.Name, Path: n.Path, Type: n.Type, Size: n.Size, MTime: n.MTime})
	}
	return out
}

// parseLsEntriesFull turns raw `restic ls --json <snapshot>` output into
// every file and directory node at any depth -- unlike parseLsEntries,
// nothing is filtered to a single level. Used to build the permanent
// snapshot catalog, where the whole tree is wanted in one pass. Only
// name/path/type/size/mtime ever survive, same as parseLsEntries.
func parseLsEntriesFull(data []byte) []ports.SnapshotEntry {
	var out []ports.SnapshotEntry
	for _, raw := range parseJSONLines(data) {
		var n lsLineJSON
		if err := json.Unmarshal(raw, &n); err != nil {
			continue
		}
		if n.Name == "" || n.Path == "" {
			continue // the leading snapshot-summary line, not a real node
		}
		out = append(out, ports.SnapshotEntry{Name: n.Name, Path: n.Path, Type: n.Type, Size: n.Size, MTime: n.MTime})
	}
	return out
}

func sanitizeDiagnostic(s string) string {
	s = domain.Redact(s)
	if len(s) > 4096 {
		return s[:4096] + "...[truncated]"
	}
	return strings.TrimSpace(s)
}
