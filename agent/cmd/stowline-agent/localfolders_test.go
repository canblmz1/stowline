package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFolderSizesAreComputedInTheBackgroundAndCached(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "alt"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 100), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "alt", "b.txt"), make([]byte, 50), 0o600)
	f := &folderSizes{}
	if first := f.Get(dir); !first.Pending {
		t.Fatalf("first call must not block on the walk: %+v", first)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := f.Get(dir)
		if !got.Pending {
			if got.Bytes != 150 || got.Files != 2 || !got.Complete {
				t.Fatalf("size = %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("walk never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAMissingFolderIsZeroNotAnError(t *testing.T) {
	f := &folderSizes{}
	missing := filepath.Join(t.TempDir(), "yok")
	f.Get(missing)
	deadline := time.Now().Add(3 * time.Second)
	for f.Get(missing).Pending {
		if time.Now().After(deadline) {
			t.Fatal("walk never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.Get(missing); got.Bytes != 0 || got.Files != 0 {
		t.Fatalf("size = %+v", got)
	}
}
