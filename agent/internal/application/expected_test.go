package application

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
)

func TestExpectedManifestMissingIsDistinctFromPass(t *testing.T) {
	_, err := LoadExpectedManifest(t.TempDir(), strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("missing snapshot expected manifest must not load")
	}
}

func TestExpectedManifestRoundTripNoLiveCorpusAlias(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "src")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	manDir := filepath.Join(dir, "manifests")
	path, hash, err := WriteExpectedManifest(manDir, expectedMeta{
		SnapshotID: strings.Repeat("c", 64),
		JobID:      "job",
		AttemptID:  "att",
		Roots:      []string{root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || path == "" {
		t.Fatal("expected hash/path")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("mutated"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadExpectedManifest(manDir, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) == 0 || got.Files[0].SHA256 == "" {
		t.Fatal("snapshot expected files missing")
	}
}

func TestWriteExpectedManifestDoesNotRewriteSourceJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, corpus.QualIncrementRel), []byte("increment-one"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.WriteManifest(root); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(root, "manifest.sha256.json")
	before, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, corpus.QualIncrementRel), []byte("increment-TWO"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteExpectedManifest(filepath.Join(t.TempDir(), "manifests"), expectedMeta{
		SnapshotID: strings.Repeat("d", 64),
		JobID:      "job",
		AttemptID:  "att",
		Roots:      []string{root},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("snapshot sidecar generation must not rewrite live manifest.sha256.json")
	}
}

func TestSnapshotSidecarUsesPreBackupHashTreeNotEmbeddedJSON(t *testing.T) {
	root := t.TempDir()
	marker := []byte("increment-live")
	if err := os.WriteFile(filepath.Join(root, corpus.QualIncrementRel), marker, 0644); err != nil {
		t.Fatal(err)
	}
	stale := []byte(`{"algorithm":"SHA-256","root":"x","files":[{"relpath":"workspace-qual-increment.txt","size":30,"sha256":"28888a51da2ef1bbe248cce3b2654fd06b191900f5357455e4794e91d565a7ac"}]}`)
	if err := os.WriteFile(filepath.Join(root, "manifest.sha256.json"), stale, 0644); err != nil {
		t.Fatal(err)
	}
	snap := strings.Repeat("e", 64)
	manDir := filepath.Join(t.TempDir(), "manifests")
	if _, _, err := WriteExpectedManifest(manDir, expectedMeta{
		SnapshotID: snap,
		JobID:      "job",
		AttemptID:  "att",
		Roots:      []string{root},
	}); err != nil {
		t.Fatal(err)
	}
	exp, err := LoadExpectedManifest(manDir, snap)
	if err != nil {
		t.Fatal(err)
	}
	var got corpus.File
	for _, f := range exp.Files {
		if f.RelPath == corpus.QualIncrementRel {
			got = f
		}
	}
	if got.Size != int64(len(marker)) || got.SHA256 == "28888a51da2ef1bbe248cce3b2654fd06b191900f5357455e4794e91d565a7ac" {
		t.Fatalf("sidecar must hash live marker, got %+v", got)
	}
	live, err := os.ReadFile(filepath.Join(root, "manifest.sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live, stale) {
		t.Fatal("embedded json must remain untouched so ordering bugs stay visible until WriteManifest")
	}
}

type treeSpyEngine struct {
	okEngine
	root           string
	jsonAtBackup   []byte
	markerAtBackup []byte
}

func (e *treeSpyEngine) Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
	e.jsonAtBackup, _ = os.ReadFile(filepath.Join(e.root, "manifest.sha256.json"))
	e.markerAtBackup, _ = os.ReadFile(filepath.Join(e.root, corpus.QualIncrementRel))
	return e.okEngine.Backup(ctx, req)
}

func TestBackupFreezesHashTreeBeforeEngineAndLeavesSourceJSON(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	root := t.TempDir()
	marker := []byte("increment-before")
	if err := os.WriteFile(filepath.Join(root, corpus.QualIncrementRel), marker, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.WriteManifest(root); err != nil {
		t.Fatal(err)
	}
	jsonBefore, err := os.ReadFile(filepath.Join(root, "manifest.sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &treeSpyEngine{root: root}
	manDir := filepath.Join(t.TempDir(), "manifests")
	s := &Service{Engine: eng, Journal: j, ManifestDir: manDir}
	res, err := s.Backup(context.Background(), domain.BackupRequest{
		Repository:  storage.LocalDescriptor(t.TempDir(), "g", "d"),
		SourceRoots: []string{root},
		VSSMode:     domain.VSSDisabled,
	})
	if err != nil || res.Outcome != domain.PhaseSucceeded {
		t.Fatalf("backup: %v %+v", err, res)
	}
	if !bytes.Equal(eng.markerAtBackup, marker) {
		t.Fatal("engine must see the pre-backup marker")
	}
	if !bytes.Equal(eng.jsonAtBackup, jsonBefore) {
		t.Fatal("engine must see the pre-backup WriteManifest bytes")
	}
	jsonAfter, err := os.ReadFile(filepath.Join(root, "manifest.sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonBefore, jsonAfter) {
		t.Fatal("successful backup must not rewrite live manifest.sha256.json")
	}
	exp, err := LoadExpectedManifest(manDir, string(res.SnapshotID))
	if err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	if err := os.WriteFile(filepath.Join(restore, corpus.QualIncrementRel), marker, 0644); err != nil {
		t.Fatal(err)
	}
	if err := IndependentRestoreVerify(manDir, string(res.SnapshotID), restore); err != nil {
		t.Fatalf("sidecar must match captured marker: %v files=%+v", err, exp.Files)
	}
	if err := os.WriteFile(filepath.Join(restore, corpus.QualIncrementRel), []byte("increment-after"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := IndependentRestoreVerify(manDir, string(res.SnapshotID), restore); err == nil {
		t.Fatal("post-generation marker change must fail")
	}
}
