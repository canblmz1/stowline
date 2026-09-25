package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCurrentSourceRootsReadsTheLatestPilotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.json")
	if err := os.WriteFile(path, []byte(`{"source_roots":["C:\\new\\a","C:\\new\\b"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := currentSourceRoots(path, []string{`C:\old`})
	if len(got) != 2 || got[0] != `C:\new\a` || got[1] != `C:\new\b` {
		t.Fatalf("expected the on-disk roots, got %v", got)
	}
}

func TestCurrentSourceRootsFallsBackWhenTheFileIsUnreadable(t *testing.T) {
	got := currentSourceRoots(filepath.Join(t.TempDir(), "missing.json"), []string{`C:\old`})
	if len(got) != 1 || got[0] != `C:\old` {
		t.Fatalf("expected the startup fallback, got %v", got)
	}
}

func TestCurrentSourceRootsFallsBackOnAnEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.json")
	if err := os.WriteFile(path, []byte(`{"source_roots":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := currentSourceRoots(path, []string{`C:\old`})
	if len(got) != 1 || got[0] != `C:\old` {
		t.Fatalf("an empty on-disk list must never silently back up nothing, got %v", got)
	}
}

func TestSyncSourceRootsRetriesUntilTheControlPlaneAnswers(t *testing.T) {
	calls := 0
	var sent []string
	report := func(_ context.Context, roots []string) error {
		calls++
		if calls < 3 {
			return errors.New("control plane unreachable")
		}
		sent = roots
		return nil
	}
	syncSourceRootsAtStart(context.Background(), report, func() []string { return []string{"C:/Belgeler"} }, 5, time.Millisecond)
	if calls != 3 || len(sent) != 1 || sent[0] != "C:/Belgeler" {
		t.Fatalf("calls=%d sent=%v", calls, sent)
	}
}

func TestSyncSourceRootsGivesUpAfterTheAttemptLimit(t *testing.T) {
	calls := 0
	report := func(context.Context, []string) error { calls++; return errors.New("down") }
	syncSourceRootsAtStart(context.Background(), report, func() []string { return []string{"C:/Belgeler"} }, 4, time.Millisecond)
	if calls != 4 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestSyncSourceRootsSendsNothingForAnEmptyList(t *testing.T) {
	called := false
	syncSourceRootsAtStart(context.Background(), func(context.Context, []string) error { called = true; return nil }, func() []string { return nil }, 3, time.Millisecond)
	if called {
		t.Fatal("an empty list must not be reported (the server rejects it)")
	}
}

func TestCurrentExcludesReadsPilotJSONAndAlwaysKeepsOST(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.json")
	if err := os.WriteFile(path, []byte(`{"source_roots":["C:\\A"],"exclude_paths":["C:\\A\\.env","*.pem","--bad"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := currentExcludes(path)
	want := []string{`C:\A\.env`, "*.pem", "*.ost"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCurrentExcludesWithoutTheFieldStillExcludesOST(t *testing.T) {
	got := currentExcludes(filepath.Join(t.TempDir(), "missing.json"))
	if len(got) != 1 || got[0] != "*.ost" {
		t.Fatalf("got %v", got)
	}
}
