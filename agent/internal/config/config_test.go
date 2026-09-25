package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeDefault(t *testing.T, prefix []byte) string {
	t.Helper()
	d := DefaultPilot()
	d.SourceRoots = []string{`C:\Stowline-TestData`}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "pilot.json")
	if err := os.WriteFile(p, append(append([]byte{}, prefix...), b...), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAcceptsAUTF8ByteOrderMark(t *testing.T) {
	// Windows PowerShell 5.1 and older Notepad save UTF-8 with a BOM. A
	// hand-edited pilot.json must still load, not be silently replaced by
	// defaults that back up a different folder.
	p := writeDefault(t, []byte{0xEF, 0xBB, 0xBF})
	f, err := Load(p)
	if err != nil {
		t.Fatalf("BOM-prefixed config must load: %v", err)
	}
	if len(f.SourceRoots) != 1 || f.SourceRoots[0] != `C:\Stowline-TestData` {
		t.Fatalf("got %v", f.SourceRoots)
	}
}

func TestLoadWithoutBOMStillWorks(t *testing.T) {
	f, err := Load(writeDefault(t, nil))
	if err != nil || f.SourceRoots[0] != `C:\Stowline-TestData` {
		t.Fatalf("err=%v roots=%v", err, f)
	}
}
