package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	mojibakeOgrenci  = "unicode/Ã¶ÄŸrenci raporlarÄ±.txt"
	mojibakeIstanbul = "unicode/Ä°stanbul-Ã¼ÄŸÅŸÃ¶Ã§.txt"
)

func writeQualMarker(t *testing.T, root, body string) {
	t.Helper()
	p := filepath.Join(root, QualIncrementRel)
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func fileRef(t *testing.T, man *Manifest, rel string) File {
	t.Helper()
	got, ok := FileByRel(man)[rel]
	if !ok {
		t.Fatalf("missing %q in manifest", rel)
	}
	return got
}

func TestMutableMarkerMatchesSnapshotReferenceAfterWriteManifest(t *testing.T) {
	root := t.TempDir()
	body := "increment-run-aaa"
	writeQualMarker(t, root, body)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	man, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := fileRef(t, man, QualIncrementRel)
	sum := sha256.Sum256([]byte(body))
	want := hex.EncodeToString(sum[:])
	if ref.Size != int64(len(body)) || ref.SHA256 != want {
		t.Fatalf("marker ref size=%d sha=%s want size=%d sha=%s", ref.Size, ref.SHA256, len(body), want)
	}
	sidecar, err := HashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	got := fileRef(t, sidecar, QualIncrementRel)
	if got != ref {
		t.Fatalf("HashTree marker %+v != WriteManifest %+v", got, ref)
	}
}

func TestMarkerChangeAfterReferenceIsMismatch(t *testing.T) {
	root := t.TempDir()
	writeQualMarker(t, root, "increment-one")
	man, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	if err := os.WriteFile(filepath.Join(restore, QualIncrementRel), []byte("increment-one"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestored(restore, man); err != nil {
		t.Fatalf("identical marker must pass: %v", err)
	}
	if err := os.WriteFile(filepath.Join(restore, QualIncrementRel), []byte("increment-two"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestored(restore, man); err == nil {
		t.Fatal("changed marker after generation must fail independent verify")
	}
}

func TestReferenceOrderingCannotSilentlyGoStale(t *testing.T) {
	root := t.TempDir()
	writeQualMarker(t, root, "increment-one")
	if _, err := WriteManifest(root); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(root, "manifest.sha256.json")
	before, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	writeQualMarker(t, root, "increment-TWO-stale")
	stale, err := LoadManifest(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	live, err := HashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if fileRef(t, stale, QualIncrementRel).SHA256 == fileRef(t, live, QualIncrementRel).SHA256 {
		t.Fatal("embedded json must stay stale until WriteManifest runs after mutation")
	}
	afterMutation, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterMutation) {
		t.Fatal("HashTree must not rewrite source manifest.sha256.json")
	}
	fresh, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if fileRef(t, fresh, QualIncrementRel).SHA256 != fileRef(t, live, QualIncrementRel).SHA256 {
		t.Fatal("WriteManifest after mutation must match live HashTree")
	}
	if _, err := VerifyRestored(root, stale); err == nil {
		t.Fatal("stale embedded json must not independently verify mutated tree")
	}
	if _, err := VerifyRestored(root, fresh); err != nil {
		t.Fatalf("fresh reference must verify: %v", err)
	}
}

func TestUnicodePathsSurviveHashAndVerify(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		UnicodeOgrenciRel:  []byte("rapor\n"),
		UnicodeIstanbulRel: []byte("türkçe\n"),
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteManifest(root); err != nil {
		t.Fatal(err)
	}
	man, err := HashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{UnicodeOgrenciRel, UnicodeIstanbulRel} {
		ref := fileRef(t, man, rel)
		p := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("real path missing %q: %v", rel, err)
		}
		if st.Size() != ref.Size {
			t.Fatalf("%s size disk=%d ref=%d", rel, st.Size(), ref.Size)
		}
		if filepath.Base(p) != filepath.Base(filepath.FromSlash(rel)) {
			t.Fatalf("disk name %q != rel base %q", filepath.Base(p), filepath.Base(rel))
		}
	}
	body, err := os.ReadFile(filepath.Join(root, "manifest.sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("öğrenci raporları.txt")) || !bytes.Contains(body, []byte("İstanbul-üğşöç.txt")) {
		t.Fatalf("manifest json lost Unicode: %s", body)
	}
	if bytes.Contains(body, []byte("Ã¶ÄŸrenci")) || bytes.Contains(body, []byte("Ä°stanbul-Ã¼ÄŸ")) {
		t.Fatal("manifest json contains cp1252 mojibake of Unicode names")
	}
	nested := t.TempDir()
	for _, rel := range []string{UnicodeOgrenciRel, UnicodeIstanbulRel} {
		src := filepath.Join(root, filepath.FromSlash(rel))
		dst := filepath.Join(nested, "C", "Stowline", "TestCorpus", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	subset := &Manifest{Algorithm: "SHA-256", Files: []File{
		fileRef(t, man, UnicodeOgrenciRel),
		fileRef(t, man, UnicodeIstanbulRel),
	}}
	if _, err := VerifyRestored(nested, subset); err != nil {
		t.Fatalf("unicode nested restore must match: %v", err)
	}
}

func TestIndependentRequiresPathSizeAndSHA256(t *testing.T) {
	root := t.TempDir()
	writeQualMarker(t, root, "increment-one")
	man, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	if err := os.WriteFile(filepath.Join(restore, QualIncrementRel), []byte("increment-one"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestored(restore, man); err != nil {
		t.Fatalf("pass path: %v", err)
	}
	wrongSize := *man
	wrongSize.Files = append([]File(nil), man.Files...)
	for i := range wrongSize.Files {
		if wrongSize.Files[i].RelPath == QualIncrementRel {
			wrongSize.Files[i].Size++
		}
	}
	if _, err := VerifyRestored(restore, &wrongSize); err == nil {
		t.Fatal("size mismatch must fail even when the file exists")
	}
}

func TestDeliberateByteCorruptionFailsIndependent(t *testing.T) {
	root := t.TempDir()
	writeQualMarker(t, root, "increment-one")
	man, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	if err := os.WriteFile(filepath.Join(restore, QualIncrementRel), []byte("increment-onX"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestored(restore, man); err == nil {
		t.Fatal("corrupt bytes must fail")
	}
}

func TestDeliberateMissingFileFailsIndependent(t *testing.T) {
	root := t.TempDir()
	writeQualMarker(t, root, "increment-one")
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	man, err := WriteManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	if err := os.WriteFile(filepath.Join(restore, QualIncrementRel), []byte("increment-one"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestored(restore, man); err == nil {
		t.Fatal("missing other.txt must fail")
	}
}

func TestMojibakeRelpathDoesNotMatchRealUnicodeFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(UnicodeOgrenciRel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("rapor\n"), 0644); err != nil {
		t.Fatal(err)
	}
	man := &Manifest{Algorithm: "SHA-256", Files: []File{{
		RelPath: mojibakeOgrenci,
		Size:    6,
		SHA256:  "c782ec53d05204b14cb7b8b3a3a4f3a0a1b2c3d4e5f60718293a4b5c6d7e8f90",
	}}}
	if _, err := VerifyRestored(root, man); err == nil {
		t.Fatal("mojibake expected path must not match real Unicode file")
	}
	_ = mojibakeIstanbul
}

func TestEmptyIndependentReferenceFailsClosed(t *testing.T) {
	if _, err := VerifyRestored(t.TempDir(), &Manifest{Algorithm: "SHA-256"}); err == nil {
		t.Fatal("empty reference must fail closed")
	}
}

func TestManifestJSONRoundTripUTF8(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(UnicodeIstanbulRel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("türkçe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteManifest(root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if fileRef(t, &man, UnicodeIstanbulRel).RelPath != UnicodeIstanbulRel {
		t.Fatalf("json relpath %q", fileRef(t, &man, UnicodeIstanbulRel).RelPath)
	}
}
