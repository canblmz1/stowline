package desktop

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractPayloadUnpacksAndSkipsARepeatRun(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "Kurulum")
	data := makeZip(t, map[string]string{"deploy.json": "{}", "payload/wizard/bootstrap.py": "print(1)"})
	if err := ExtractPayload(data, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "payload", "wizard", "bootstrap.py")); err != nil || string(b) != "print(1)" {
		t.Fatalf("extracted = %q, %v", b, err)
	}
	// a second run with the same payload leaves the tree alone
	sentinel := filepath.Join(dest, "kept.txt")
	_ = os.WriteFile(sentinel, []byte("x"), 0o644)
	if err := ExtractPayload(data, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("an identical payload must not be re-extracted")
	}
	// a different payload replaces the tree
	if err := ExtractPayload(makeZip(t, map[string]string{"deploy.json": `{"v":2}`}), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("a new payload must replace the old tree")
	}
}

func TestExtractPayloadRefusesPathsOutsideTheTarget(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "Kurulum")
	if err := ExtractPayload(makeZip(t, map[string]string{"../evil.txt": "x"}), dest); err == nil {
		t.Fatal("a ../ entry must be refused")
	}
	if _, err := os.Stat(filepath.Join(base, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("nothing may be written outside the target")
	}
}
