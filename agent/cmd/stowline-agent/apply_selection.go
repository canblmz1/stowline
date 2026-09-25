package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

// applySelection atomically updates this device's source_roots in
// pilot.json so a new backup selection takes effect without Setup
// Wizard being rerun. Basic path shape is validated here (absolute, no
// control bytes, no duplicates); WHETHER a given path is sensitive and
// properly consented to was already enforced server-side before this
// command was ever enqueued (server/app/main.py's apply_selection) --
// sensitiveConsents is accepted only to echo back in the result for the
// audit trail, not re-checked here, matching the trust boundary every
// other command in this dispatcher already has with the control plane.
func applySelection(_ context.Context, revisionID string, sourceRoots, sensitiveConsents []string, pilotJSONPath string) (map[string]any, error) {
	if strings.TrimSpace(revisionID) == "" {
		return nil, fmt.Errorf("%w: revision_id required", domain.ErrSelectionRejected)
	}
	if len(sourceRoots) == 0 {
		return nil, fmt.Errorf("%w: at least one source root required", domain.ErrSelectionRejected)
	}
	cleaned := make([]string, 0, len(sourceRoots))
	seen := map[string]bool{}
	for _, root := range sourceRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, ch := range root {
			if ch < 0x20 {
				return nil, fmt.Errorf("%w: control byte in path", domain.ErrSelectionRejected)
			}
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("%w: absolute path required: %s", domain.ErrSelectionRejected, root)
		}
		clean := filepath.Clean(root)
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, clean)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: no valid source roots after validation", domain.ErrSelectionRejected)
	}

	// APPLY_SELECTION may be re-dispatched after the config rename succeeds
	// but before FinishCommand reaches SQLite. Persist the revision in the
	// same atomic file replacement as source_roots: an exact replay becomes
	// a no-op, while reusing a revision id with different roots fails closed.
	raw, err := os.ReadFile(pilotJSONPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read pilot.json: %v", domain.ErrSelectionRejected, err)
	}
	var current map[string]any
	if err := json.Unmarshal(config.StripBOM(raw), &current); err != nil {
		return nil, fmt.Errorf("%w: parse pilot.json: %v", domain.ErrSelectionRejected, err)
	}
	if appliedRevision, _ := current["selection_revision_id"].(string); appliedRevision == revisionID {
		currentRoots, ok := stringList(current["source_roots"])
		if !ok || !equalStrings(currentRoots, cleaned) {
			return nil, fmt.Errorf("%w: revision_id already applied with different source_roots", domain.ErrSelectionRejected)
		}
		return selectionResult(revisionID, cleaned, sensitiveConsents), nil
	}

	err = atomicUpdatePilotJSON(pilotJSONPath, func(data map[string]any) {
		roots := make([]any, len(cleaned))
		for i, v := range cleaned {
			roots[i] = v
		}
		data["source_roots"] = roots
		data["selection_revision_id"] = revisionID
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrSelectionRejected, err)
	}

	return selectionResult(revisionID, cleaned, sensitiveConsents), nil
}

func selectionResult(revisionID string, roots, sensitiveConsents []string) map[string]any {
	return map[string]any{
		"revision_id":        revisionID,
		"source_roots":       append([]string(nil), roots...),
		"source_root_count":  len(roots),
		"sensitive_consents": sensitiveConsents,
	}
}

func stringList(value any) ([]string, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// atomicUpdatePilotJSON reads the existing pilot.json, lets mutate apply
// an in-memory change, then writes the result to a sibling temp file
// and renames it into place. os.Rename onto an existing destination on
// the same volume is atomic on Windows (NTFS) exactly as it is on
// POSIX, so a crash or power loss mid-write can never leave pilot.json
// truncated or half-written -- a reader always sees either the fully
// old or the fully new file. This replaces persistConfig's (ops.go)
// direct os.WriteFile for any caller that cannot tolerate a corrupt
// config surviving a crash between open and write completing.
func atomicUpdatePilotJSON(path string, mutate func(map[string]any)) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pilot.json: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(config.StripBOM(raw), &data); err != nil {
		return fmt.Errorf("parse pilot.json: %w", err)
	}
	mutate(data)
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pilot.json: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pilot-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp config into place: %w", err)
	}
	return nil
}
