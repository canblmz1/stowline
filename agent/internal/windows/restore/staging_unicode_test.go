package restore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
)

func TestResultManifestRelpathEqualsRestoredRelpathUnicode(t *testing.T) {
	stage, err := Prepare(t.TempDir(), "ws-restore-unicode")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(stage.Path, "C", "Stowline", "TestCorpus")
	files := map[string][]byte{
		corpus.UnicodeOgrenciRel:  []byte("rapor\n"),
		corpus.UnicodeIstanbulRel: []byte("türkçe\n"),
	}
	var diskRels []string
	for rel, body := range files {
		p := filepath.Join(nested, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		gotRel, err := filepath.Rel(stage.Path, p)
		if err != nil {
			t.Fatal(err)
		}
		diskRels = append(diskRels, filepath.ToSlash(gotRel))
	}
	path, man, err := WriteManifest(stage, domain.SnapshotID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Ã¶ÄŸrenci")) || bytes.Contains(raw, []byte("Ä°stanbul-Ã¼ÄŸ")) {
		t.Fatalf("result-manifest.json serialized mojibake: %s", raw)
	}
	if !bytes.Contains(raw, []byte("öğrenci raporları.txt")) || !bytes.Contains(raw, []byte("İstanbul-üğşöç.txt")) {
		t.Fatalf("result-manifest.json lost Unicode names: %s", raw)
	}
	var decoded ResultManifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	byRel := map[string]ManifestFile{}
	for _, f := range decoded.Files {
		byRel[f.RelPath] = f
	}
	for _, rel := range diskRels {
		got, ok := byRel[rel]
		if !ok {
			t.Fatalf("result-manifest relpath %q missing; have %#v", rel, byRel)
		}
		if got.RelPath != rel {
			t.Fatalf("relpath %q != disk %q", got.RelPath, rel)
		}
	}
	if err := CompareIndependent(stage.Path, man.Files); err != nil {
		t.Fatalf("independent vs result-manifest: %v", err)
	}
}

func TestCompareIndependentCorruptionAndMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, sz, err := hashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []ManifestFile{{RelPath: "a.txt", Size: sz, SHA256: sum}}
	if err := CompareIndependent(dir, want); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CompareIndependent(dir, want); err == nil {
		t.Fatal("corrupt bytes must fail")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := CompareIndependent(dir, want); err == nil {
		t.Fatal("missing file must fail")
	}
	if err := CompareIndependent(dir, nil); err == nil {
		t.Fatal("empty expected must fail closed")
	}
}
