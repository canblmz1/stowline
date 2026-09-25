package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseLocalDirListsAndSortsEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.txt", "a.txt", "sub"} {
		if name == "sub" {
			if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := browseLocalDir(context.Background(), dir, "", 200, "")
	if err != nil {
		t.Fatal(err)
	}
	entries := out["entries"].([]map[string]any)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(entries), entries)
	}
	if entries[0]["name"] != "a.txt" || entries[1]["name"] != "b.txt" || entries[2]["name"] != "sub" {
		t.Fatalf("expected alphabetical order, got %v", entries)
	}
	if entries[2]["type"] != "directory" {
		t.Fatalf("sub must be reported as a directory: %v", entries[2])
	}
	if out["path"] != dir {
		t.Fatalf("path echo mismatch: %v", out["path"])
	}
}

func TestBrowseLocalDirBoundsResultsAndPaginatesViaCursor(t *testing.T) {
	dir := t.TempDir()
	names := []string{"a", "b", "c", "d", "e"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n+".txt"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := browseLocalDir(context.Background(), dir, "", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	firstEntries := first["entries"].([]map[string]any)
	if len(firstEntries) != 2 {
		t.Fatalf("expected limit=2 to cap the page, got %d", len(firstEntries))
	}
	cursor, _ := first["cursor"].(string)
	if cursor == "" {
		t.Fatal("expected a cursor when more entries remain")
	}

	second, err := browseLocalDir(context.Background(), dir, cursor, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	secondEntries := second["entries"].([]map[string]any)
	if len(secondEntries) != 3 {
		t.Fatalf("expected the remaining 3 entries after the cursor, got %d: %v", len(secondEntries), secondEntries)
	}
	if secondEntries[0]["name"] == firstEntries[0]["name"] || secondEntries[0]["name"] == firstEntries[1]["name"] {
		t.Fatalf("second page overlaps the first: %v / %v", firstEntries, secondEntries)
	}
}

func TestBrowseLocalDirRejectsRelativePath(t *testing.T) {
	if _, err := browseLocalDir(context.Background(), `relative\path`, "", 200, ""); err == nil {
		t.Fatal("relative path must be rejected")
	}
}

func TestBrowseLocalDirRejectsControlBytesAndEmpty(t *testing.T) {
	if _, err := browseLocalDir(context.Background(), "", "", 200, ""); err == nil {
		t.Fatal("empty path must be rejected")
	}
	if _, err := browseLocalDir(context.Background(), "C:\\Users\\op\x00evil", "", 200, ""); err == nil {
		t.Fatal("NUL byte must be rejected")
	}
}

func TestBrowseLocalDirRejectsNonexistentPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := browseLocalDir(context.Background(), filepath.Join(dir, "does-not-exist"), "", 200, ""); err == nil {
		t.Fatal("nonexistent path must be rejected, not silently return empty")
	}
}

func TestBrowseLocalDirRejectsFileAsTarget(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := browseLocalDir(context.Background(), f, "", 200, ""); err == nil {
		t.Fatal("a file (not a directory) must be rejected")
	}
}

func TestBrowseLocalDirBlocksSecretsAndConfigUnderPilotRoot(t *testing.T) {
	pilotRoot := t.TempDir()
	secrets := filepath.Join(pilotRoot, "secrets")
	config := filepath.Join(pilotRoot, "config")
	restore := filepath.Join(pilotRoot, "Restore")
	if err := os.MkdirAll(secrets, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(restore, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := browseLocalDir(context.Background(), secrets, "", 200, pilotRoot); err == nil {
		t.Fatal("secrets directory must never be browsable")
	}
	if _, err := browseLocalDir(context.Background(), config, "", 200, pilotRoot); err == nil {
		t.Fatal("config directory must never be browsable")
	}
	if _, err := browseLocalDir(context.Background(), restore, "", 200, pilotRoot); err != nil {
		t.Fatalf("an unrelated pilot subdirectory must stay browsable: %v", err)
	}
}
