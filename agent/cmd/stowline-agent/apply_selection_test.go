package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writePilotJSON(t *testing.T, dir string, extra map[string]any) string {
	t.Helper()
	data := map[string]any{"device_id": "pilot-device", "source_roots": []any{`C:\Stowline\TestCorpus`}}
	for k, v := range extra {
		data[k] = v
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pilot.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSourceRoots(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	rawRoots, _ := data["source_roots"].([]any)
	out := make([]string, len(rawRoots))
	for i, r := range rawRoots {
		out[i], _ = r.(string)
	}
	return out
}

func TestApplySelectionWritesNewRootsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	res, err := applySelection(context.Background(), "rev-1", []string{`C:\Users\op\Desktop`, `C:\Users\op\Documents`}, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if res["revision_id"] != "rev-1" {
		t.Fatalf("revision_id not echoed: %v", res)
	}
	if roots, ok := res["source_roots"].([]string); !ok || len(roots) != 2 {
		t.Fatalf("exact applied roots not returned: %v", res)
	}
	got := readSourceRoots(t, path)
	if len(got) != 2 || got[0] != `C:\Users\op\Desktop` || got[1] != `C:\Users\op\Documents` {
		t.Fatalf("source_roots not applied: %v", got)
	}
	// Everything else in pilot.json must survive untouched.
	raw, _ := os.ReadFile(path)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	if data["device_id"] != "pilot-device" {
		t.Fatalf("unrelated pilot.json fields must not be dropped: %v", data)
	}
	if data["selection_revision_id"] != "rev-1" {
		t.Fatalf("applied revision must be persisted atomically with roots: %v", data)
	}
}

func TestApplySelectionSameRevisionAndRootsIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	roots := []string{`C:\Users\op\Desktop`, `C:\Users\op\Documents`}
	first, err := applySelection(context.Background(), "rev-replay", roots, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve a harmless formatting difference to prove the replay did not
	// rewrite pilot.json after recognizing the already-applied revision.
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := applySelection(context.Background(), "rev-replay", roots, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("same revision + exact roots must not rewrite pilot.json")
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("idempotent replay result changed: first=%s second=%s", firstJSON, secondJSON)
	}
}

func TestApplySelectionRejectsRevisionReuseWithDifferentRoots(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	if _, err := applySelection(context.Background(), "rev-conflict", []string{`C:\Users\op\Desktop`}, nil, path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := applySelection(context.Background(), "rev-conflict", []string{`C:\Users\op\Documents`}, nil, path); err == nil {
		t.Fatal("same revision with different roots must fail closed")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("conflicting revision replay must not rewrite pilot.json")
	}
}

func TestApplySelectionRequiresRevisionID(t *testing.T) {
	path := writePilotJSON(t, t.TempDir(), nil)
	if _, err := applySelection(context.Background(), "", []string{`C:\Users\op\Desktop`}, nil, path); err == nil {
		t.Fatal("missing revision_id must fail closed")
	}
}

func TestApplySelectionRejectsRelativeAndKeepsOriginalConfig(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	before, _ := os.ReadFile(path)

	if _, err := applySelection(context.Background(), "rev-2", []string{`relative\path`}, nil, path); err == nil {
		t.Fatal("relative source root must be rejected")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("pilot.json must be untouched when validation fails")
	}
}

func TestApplySelectionRejectsEmptyRootList(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	if _, err := applySelection(context.Background(), "rev-3", nil, nil, path); err == nil {
		t.Fatal("empty source_roots must be rejected")
	}
	if _, err := applySelection(context.Background(), "rev-3", []string{"   "}, nil, path); err == nil {
		t.Fatal("whitespace-only source root must leave nothing valid and be rejected")
	}
}

func TestApplySelectionDeduplicatesCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	_, err := applySelection(context.Background(), "rev-4", []string{`C:\Users\op\Desktop`, `c:\users\op\desktop`}, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	got := readSourceRoots(t, path)
	if len(got) != 1 {
		t.Fatalf("expected case-insensitive de-dup to leave exactly one root, got %v", got)
	}
}

func TestAtomicUpdatePilotJSONNeverLeavesATempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := writePilotJSON(t, dir, nil)
	if err := atomicUpdatePilotJSON(path, func(data map[string]any) { data["x"] = "y" }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "pilot.json" {
			t.Fatalf("unexpected leftover file after a successful atomic update: %s", e.Name())
		}
	}
}

func TestAtomicUpdatePilotJSONFailsCleanlyOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "pilot.json")
	if err := atomicUpdatePilotJSON(missing, func(map[string]any) {}); err == nil {
		t.Fatal("updating a nonexistent pilot.json must fail, not create one from nothing")
	}
}
