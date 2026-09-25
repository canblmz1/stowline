package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/paths"
)

const browseLocalDirMaxLimit = 1000

// browseLocalDir lists the immediate entries of one directory -- single
// level only, never recurses, never reads a file's content, and never
// follows a child reparse point/junction into a listing of what it
// points to (the child is reported with type "reparse", not walked).
// pilotRoot confines the two subtrees genuinely off-limits to a remote
// admin operator (DPAPI secrets and the agent's own config), not the
// listing as a whole -- BROWSE_LOCAL_DIR is meant to reach anywhere an
// operator might pick a real backup source from, unlike RESTORE_TO_STAGING
// which must stay inside one approved root.
func browseLocalDir(_ context.Context, rawPath, cursor string, limit int, pilotRoot string) (map[string]any, error) {
	if limit <= 0 || limit > browseLocalDirMaxLimit {
		limit = browseLocalDirMaxLimit
	}
	clean, err := safeBrowseDir(rawPath, pilotRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		switch {
		case os.IsPermission(err):
			return nil, fmt.Errorf("%w: access denied", domain.ErrBrowsePathRejected)
		case os.IsNotExist(err):
			return nil, fmt.Errorf("%w: not found", domain.ErrBrowsePathRejected)
		default:
			return nil, fmt.Errorf("%w: %v", domain.ErrBrowsePathRejected, err)
		}
	}

	names := make([]string, 0, len(entries))
	byName := make(map[string]os.DirEntry, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		byName[e.Name()] = e
	}
	sort.Strings(names)

	start := 0
	if cursor != "" {
		for i, n := range names {
			if n > cursor {
				start = i
				break
			}
			start = i + 1
		}
	}

	out := make([]map[string]any, 0, limit)
	nextCursor := ""
	for i := start; i < len(names); i++ {
		if len(out) >= limit {
			nextCursor = names[i-1]
			break
		}
		name := names[i]
		entry := byName[name]
		full := filepath.Join(clean, name)
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		if paths.IsReparse(full) {
			kind = "reparse"
		}
		out = append(out, map[string]any{"name": name, "path": full, "type": kind})
	}

	parent := filepath.Dir(clean)
	if parent == clean {
		parent = ""
	}
	result := map[string]any{"path": clean, "parent": parent, "entries": out}
	if nextCursor != "" {
		result["cursor"] = nextCursor
	}
	return result, nil
}

// safeBrowseDir validates a client-supplied browse target before it is
// ever handed to os.ReadDir: absolute path required, no NUL/control
// bytes, must exist and be a real directory, and must not be the
// agent's own secrets or config directory (DPAPI envelopes, pilot.json)
// -- an operator browsing for backup SOURCES has no legitimate reason to
// need either, and both hold data this command must never surface.
func safeBrowseDir(rawPath, pilotRoot string) (string, error) {
	raw := strings.TrimSpace(rawPath)
	if raw == "" {
		return "", fmt.Errorf("%w: empty path", domain.ErrBrowsePathRejected)
	}
	for _, ch := range raw {
		if ch < 0x20 {
			return "", fmt.Errorf("%w: control byte in path", domain.ErrBrowsePathRejected)
		}
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%w: absolute path required", domain.ErrBrowsePathRejected)
	}
	clean := filepath.Clean(raw)

	if pilotRoot != "" {
		root := filepath.Clean(pilotRoot)
		for _, off := range []string{filepath.Join(root, "secrets"), filepath.Join(root, "config")} {
			if strings.EqualFold(clean, off) || strings.HasPrefix(strings.ToLower(clean), strings.ToLower(off)+string(filepath.Separator)) {
				return "", fmt.Errorf("%w: this directory is not browsable", domain.ErrBrowsePathRejected)
			}
		}
	}

	info, err := os.Stat(clean)
	if err != nil {
		if os.IsPermission(err) {
			return "", fmt.Errorf("%w: access denied", domain.ErrBrowsePathRejected)
		}
		return "", fmt.Errorf("%w: not found", domain.ErrBrowsePathRejected)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: not a directory", domain.ErrBrowsePathRejected)
	}
	return clean, nil
}
