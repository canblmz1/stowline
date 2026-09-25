package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
)

type expectedMeta struct {
	SnapshotID string
	JobID      string
	AttemptID  string
	Roots      []string
	Files      []corpus.File
}

// SnapshotExpected is the immutable synthetic-corpus proof associated with one snapshot.
type SnapshotExpected struct {
	SnapshotID     string        `json:"snapshot_id"`
	JobID          string        `json:"job_id"`
	AttemptID      string        `json:"attempt_id"`
	CreatedAt      string        `json:"created_at"`
	SourceRoots    []string      `json:"source_roots"`
	ManifestSHA256 string        `json:"manifest_sha256"`
	Files          []corpus.File `json:"files"`
	SyntheticPilot bool          `json:"synthetic_pilot"`
}

func hashSourceRoots(roots []string) ([]corpus.File, error) {
	var files []corpus.File
	for _, root := range roots {
		man, err := corpus.HashTree(root)
		if err != nil {
			return nil, err
		}
		files = append(files, man.Files...)
	}
	return files, nil
}

func marshalExpectedUTF8(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func WriteExpectedManifest(dir string, meta expectedMeta) (path, hash string, err error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	files := meta.Files
	if files == nil {
		files, err = hashSourceRoots(meta.Roots)
		if err != nil {
			return "", "", err
		}
	}
	exp := SnapshotExpected{
		SnapshotID:     meta.SnapshotID,
		JobID:          meta.JobID,
		AttemptID:      meta.AttemptID,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SourceRoots:    append([]string(nil), meta.Roots...),
		Files:          files,
		SyntheticPilot: true,
	}
	body, err := json.Marshal(exp)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	exp.ManifestSHA256 = hex.EncodeToString(sum[:])
	out, err := marshalExpectedUTF8(exp)
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, string(meta.SnapshotID)+".json")
	if err := os.WriteFile(path, out, 0600); err != nil {
		return "", "", err
	}
	return path, exp.ManifestSHA256, nil
}

// IndependentRestoreVerify checks restored bytes against the snapshot-specific
// sidecar. It does not use an embedded corpus manifest.sha256.json.
func IndependentRestoreVerify(manifestDir, snapshotID, restoredRoot string) error {
	if strings.TrimSpace(manifestDir) == "" || strings.TrimSpace(snapshotID) == "" {
		return fmt.Errorf("missing snapshot independent reference")
	}
	exp, err := LoadExpectedManifest(manifestDir, snapshotID)
	if err != nil {
		return err
	}
	if len(exp.Files) == 0 {
		return fmt.Errorf("empty snapshot independent reference")
	}
	_, err = corpus.VerifyRestored(restoredRoot, &corpus.Manifest{Algorithm: "SHA-256", Files: exp.Files})
	return err
}

func LoadExpectedManifest(dir, snapshotID string) (*SnapshotExpected, error) {
	b, err := os.ReadFile(filepath.Join(dir, snapshotID+".json"))
	if err != nil {
		return nil, err
	}
	var exp SnapshotExpected
	if err := json.Unmarshal(b, &exp); err != nil {
		return nil, err
	}
	return &exp, nil
}
