package corpus

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	QualIncrementRel   = "workspace-qual-increment.txt"
	UnicodeIstanbulRel = "unicode/İstanbul-üğşöç.txt"
	UnicodeOgrenciRel  = "unicode/öğrenci raporları.txt"
)

type File struct {
	RelPath string `json:"relpath"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	Algorithm string `json:"algorithm"`
	Root      string `json:"root"`
	Files     []File `json:"files"`
}

func Generate(root string) (*Manifest, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	write := func(rel string, data []byte) error {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0644)
	}
	if err := write("README.txt", []byte("Stowline synthetic pilot corpus. Not company data.\n")); err != nil {
		return nil, err
	}
	if err := write("zero.txt", []byte{}); err != nil {
		return nil, err
	}
	if err := write("small.txt", []byte("hello stowline backup\n")); err != nil {
		return nil, err
	}
	bin := make([]byte, 256*1024)
	if _, err := rand.Read(bin); err != nil {
		return nil, err
	}
	if err := write("binary.bin", bin); err != nil {
		return nil, err
	}
	if err := write("nested/a/b/c/file.txt", []byte("nested\n")); err != nil {
		return nil, err
	}
	dup := []byte("duplicate-content-abc")
	if err := write("dup/one.txt", dup); err != nil {
		return nil, err
	}
	if err := write("dup/two.txt", dup); err != nil {
		return nil, err
	}
	if err := write(UnicodeIstanbulRel, []byte("türkçe\n")); err != nil {
		return nil, err
	}
	if err := write(UnicodeOgrenciRel, []byte("rapor\n")); err != nil {
		return nil, err
	}
	if err := write("spaces/file with spaces.txt", []byte("spaces\n")); err != nil {
		return nil, err
	}
	longDir := "long/" + strings.Repeat("a", 80)
	if err := write(longDir+"/end.txt", []byte("long\n")); err != nil {
		return nil, err
	}
	large := make([]byte, 16<<20)
	if _, err := rand.Read(large); err != nil {
		return nil, err
	}
	if err := write("large/16mb.bin", large); err != nil {
		return nil, err
	}
	for i := 0; i < 200; i++ {
		if err := write(fmt.Sprintf("many/%03d.txt", i), []byte(fmt.Sprintf("file-%d\n", i))); err != nil {
			return nil, err
		}
	}
	if err := write("versions/note.txt", []byte("version-A\n")); err != nil {
		return nil, err
	}
	if err := write("TEST-DELETE-ME.txt", []byte("this generated fixture may be deleted in tests\n")); err != nil {
		return nil, err
	}
	return WriteManifest(root)
}

func skipIndependentName(name string) bool {
	switch strings.ToLower(name) {
	case "manifest.sha256.json", "result-manifest.json", ".stowline-staging":
		return true
	default:
		return false
	}
}

func pathMatchesRel(fullSlash, rel string) bool {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" {
		return false
	}
	return fullSlash == rel || strings.HasSuffix(fullSlash, "/"+rel)
}

func marshalPrettyUTF8(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// HashTree returns size and SHA-256 for every file under root without writing
// manifest.sha256.json. The walk uses OS filesystem names (UTF-16 on Windows)
// and stores relpaths as slash-separated UTF-8.
func HashTree(root string) (*Manifest, error) {
	man := &Manifest{Algorithm: "SHA-256", Root: root}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.EqualFold(info.Name(), ".stowline-staging") {
				return filepath.SkipDir
			}
			return nil
		}
		if skipIndependentName(info.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		sum, sz, err := hash(p)
		if err != nil {
			return err
		}
		man.Files = append(man.Files, File{RelPath: filepath.ToSlash(rel), Size: sz, SHA256: sum})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return man, nil
}

func WriteManifest(root string) (*Manifest, error) {
	man, err := HashTree(root)
	if err != nil {
		return nil, err
	}
	b, err := marshalPrettyUTF8(man)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.sha256.json"), b, 0644); err != nil {
		return nil, err
	}
	return man, nil
}

func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func Map(m *Manifest) map[string]string {
	out := make(map[string]string, len(m.Files))
	for _, f := range m.Files {
		out[f.RelPath] = f.SHA256
	}
	return out
}

func hash(path string) (string, int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b)), nil
}

func FileByRel(m *Manifest) map[string]File {
	out := make(map[string]File, len(m.Files))
	if m == nil {
		return out
	}
	for _, f := range m.Files {
		out[filepath.ToSlash(f.RelPath)] = f
	}
	return out
}

func VerifyRestored(restoreRoot string, expected *Manifest) (matched int, err error) {
	if expected == nil || len(expected.Files) == 0 {
		return 0, fmt.Errorf("empty independent reference")
	}
	want := FileByRel(expected)
	type foundFile struct {
		size int64
		sum  string
	}
	found := map[string]foundFile{}
	err = filepath.Walk(restoreRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil {
			return nil
		}
		if info.IsDir() {
			if strings.EqualFold(info.Name(), ".stowline-staging") {
				return filepath.SkipDir
			}
			return nil
		}
		if skipIndependentName(info.Name()) {
			return nil
		}
		sum, sz, err := hash(p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(p)
		for rel := range want {
			if pathMatchesRel(slash, rel) {
				found[rel] = foundFile{size: sz, sum: sum}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var missing, mismatch []string
	for rel, wantFile := range want {
		if skipIndependentName(filepath.Base(rel)) {
			continue
		}
		got, ok := found[rel]
		if !ok {
			missing = append(missing, rel)
			continue
		}
		if got.sum != wantFile.SHA256 || got.size != wantFile.Size {
			mismatch = append(mismatch, rel)
			continue
		}
		matched++
	}
	if len(missing) > 0 || len(mismatch) > 0 {
		return matched, fmt.Errorf("restore checksum mismatch missing=%d mismatch=%d matched=%d", len(missing), len(mismatch), matched)
	}
	return matched, nil
}

func MutateForSnapshotB(root string) error {
	p := filepath.Join(root, "versions", "note.txt")
	if err := os.WriteFile(p, []byte("version-B\n"), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "versions", "added-B.txt"), []byte("new in B\n"), 0644)
}
