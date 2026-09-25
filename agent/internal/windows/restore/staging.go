package restore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
	"github.com/canblmz1/stowline/agent/internal/windows/paths"
)

type StagingDir struct {
	Root string
	Job  domain.JobID
	Path string
}

func Prepare(stagingRoot string, job domain.JobID) (StagingDir, error) {
	if stagingRoot == "" || job == "" {
		return StagingDir{}, domain.ErrRestorePathRejected
	}
	dest := filepath.Join(stagingRoot, string(job))
	if err := paths.ValidateRestoreDestination(stagingRoot, dest); err != nil {
		return StagingDir{}, err
	}
	if err := paths.ValidateStagingRoot(stagingRoot); err != nil {
		return StagingDir{}, err
	}
	if paths.IsForbiddenTarget(dest) {
		return StagingDir{}, domain.ErrRestorePathRejected
	}
	if st, err := os.Stat(dest); err == nil {
		if !st.IsDir() {
			return StagingDir{}, domain.ErrRestorePathRejected
		}
	} else {
		if err := os.MkdirAll(dest, 0700); err != nil {
			return StagingDir{}, err
		}
	}
	_ = os.MkdirAll(dest, 0700)
	_ = acl.Apply(stagingRoot, acl.RestoreStaging)
	marker := filepath.Join(dest, ".stowline-staging")
	meta := map[string]string{
		"job_id":         string(job),
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"do_not_execute": "true",
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(marker, b, 0600); err != nil {
		return StagingDir{}, err
	}
	return StagingDir{Root: stagingRoot, Job: job, Path: dest}, nil
}

func Revalidate(dir StagingDir) error {
	if err := paths.ValidateStagingRoot(dir.Root); err != nil {
		return err
	}
	return paths.ValidateRestoreDestination(dir.Root, dir.Path)
}

type ManifestFile struct {
	RelPath string `json:"relpath"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

type ResultManifest struct {
	JobID        string         `json:"job_id"`
	SnapshotID   string         `json:"snapshot_id"`
	Destination  string         `json:"destination"`
	CreatedAt    string         `json:"created_at"`
	EngineVerify bool           `json:"engine_verify"`
	Files        []ManifestFile `json:"files"`
	Incomplete   bool           `json:"incomplete"`
}

func WriteManifest(dir StagingDir, snap domain.SnapshotID, engineVerify bool) (string, *ResultManifest, error) {
	man := &ResultManifest{
		JobID:        string(dir.Job),
		SnapshotID:   string(snap),
		Destination:  dir.Path,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		EngineVerify: engineVerify,
	}
	err := filepath.Walk(dir.Path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.EqualFold(info.Name(), ".stowline-staging") {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(p)
		if strings.EqualFold(base, ".stowline-staging") || strings.EqualFold(base, "result-manifest.json") {
			return nil
		}
		rel, err := filepath.Rel(dir.Path, p)
		if err != nil {
			return err
		}
		sum, sz, err := hashFile(p)
		if err != nil {
			return err
		}
		man.Files = append(man.Files, ManifestFile{RelPath: filepath.ToSlash(rel), Size: sz, SHA256: sum})
		return nil
	})
	if err != nil {
		return "", man, err
	}
	path := filepath.Join(dir.Path, "result-manifest.json")
	b, err := marshalPrettyUTF8(man)
	if err != nil {
		return "", man, err
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		return "", man, err
	}
	return path, man, nil
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

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func pathMatchesRel(fullSlash, rel string) bool {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" {
		return false
	}
	return fullSlash == rel || strings.HasSuffix(fullSlash, "/"+rel)
}

func CompareIndependent(restoredRoot string, expected []ManifestFile) error {
	if len(expected) == 0 {
		return fmt.Errorf("%w: empty independent reference", domain.ErrIncompleteStaging)
	}
	type foundFile struct {
		size int64
		sum  string
	}
	found := map[string]foundFile{}
	err := filepath.Walk(restoredRoot, func(p string, info os.FileInfo, err error) error {
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
		base := filepath.Base(p)
		if strings.EqualFold(base, ".stowline-staging") || strings.EqualFold(base, "result-manifest.json") {
			return nil
		}
		sum, sz, err := hashFile(p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(p)
		for _, want := range expected {
			if pathMatchesRel(slash, want.RelPath) {
				found[filepath.ToSlash(want.RelPath)] = foundFile{size: sz, sum: sum}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	var mismatch []string
	for _, want := range expected {
		rel := filepath.ToSlash(want.RelPath)
		got, ok := found[rel]
		if !ok {
			missing = append(missing, rel)
			continue
		}
		if got.sum != want.SHA256 || got.size != want.Size {
			mismatch = append(mismatch, rel)
		}
	}
	if len(missing) > 0 || len(mismatch) > 0 {
		return fmt.Errorf("%w: missing=%d mismatch=%d", domain.ErrIncompleteStaging, len(missing), len(mismatch))
	}
	return nil
}
