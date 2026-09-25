package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
)

type populateRestoreEngine struct {
	okEngine
	write map[string][]byte
}

func (e *populateRestoreEngine) Restore(_ context.Context, req domain.RestoreRequest) (domain.RestoreResult, error) {
	for rel, data := range e.write {
		p := filepath.Join(req.DestinationDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return domain.RestoreResult{State: domain.RestoreFailed, ErrorMessage: err.Error()}, err
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			return domain.RestoreResult{State: domain.RestoreFailed, ErrorMessage: err.Error()}, err
		}
	}
	return domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}, nil
}

func TestRestoreIndependentOKMatchesSnapshotSidecarUnicode(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ogrenci := []byte("rapor\n")
	istanbul := []byte("türkçe\n")
	marker := []byte("increment-one")
	manDir := t.TempDir()
	snap := strings.Repeat("a", 64)
	src := t.TempDir()
	writeTree := map[string][]byte{
		corpus.QualIncrementRel:   marker,
		corpus.UnicodeOgrenciRel:  ogrenci,
		corpus.UnicodeIstanbulRel: istanbul,
	}
	for rel, body := range writeTree {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := WriteExpectedManifest(manDir, expectedMeta{
		SnapshotID: snap,
		JobID:      "job",
		AttemptID:  "att",
		Roots:      []string{src},
	}); err != nil {
		t.Fatal(err)
	}
	nestedWrite := map[string][]byte{}
	for rel, body := range writeTree {
		nestedWrite["C/Stowline/TestCorpus/"+rel] = body
	}
	s := &Service{
		Engine:      &populateRestoreEngine{write: nestedWrite},
		Journal:     j,
		ManifestDir: manDir,
	}
	res, err := s.Restore(context.Background(), restoreReq(t, "ws-restore-ok", snap))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.RestoreReady {
		t.Fatalf("state %s %s", res.State, res.ErrorMessage)
	}
	if !res.IndependentOK {
		t.Fatal("IndependentOK must be true when sidecar size and SHA-256 match restored Unicode paths")
	}
}

func TestRestoreIndependentOKFalseOnCorruption(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	manDir := t.TempDir()
	snap := strings.Repeat("b", 64)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, corpus.QualIncrementRel), []byte("increment-one"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteExpectedManifest(manDir, expectedMeta{
		SnapshotID: snap, JobID: "job", AttemptID: "att", Roots: []string{src},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		Engine: &populateRestoreEngine{write: map[string][]byte{
			"C/Stowline/TestCorpus/" + corpus.QualIncrementRel: []byte("increment-bad"),
		}},
		Journal:     j,
		ManifestDir: manDir,
	}
	res, err := s.Restore(context.Background(), restoreReq(t, "ws-restore-corrupt", snap))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.RestoreReady {
		t.Fatalf("engine-verified restore stays READY, got %s", res.State)
	}
	if res.IndependentOK {
		t.Fatal("corrupt bytes must set IndependentOK=false")
	}
}

func TestRestoreIndependentOKFalseOnMissingFile(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	manDir := t.TempDir()
	snap := strings.Repeat("c", 64)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, corpus.QualIncrementRel), []byte("increment-one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "other.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteExpectedManifest(manDir, expectedMeta{
		SnapshotID: snap, JobID: "job", AttemptID: "att", Roots: []string{src},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		Engine: &populateRestoreEngine{write: map[string][]byte{
			"C/Stowline/TestCorpus/" + corpus.QualIncrementRel: []byte("increment-one"),
		}},
		Journal:     j,
		ManifestDir: manDir,
	}
	res, err := s.Restore(context.Background(), restoreReq(t, "ws-restore-missing", snap))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.RestoreReady {
		t.Fatalf("engine-verified restore stays READY, got %s", res.State)
	}
	if res.IndependentOK {
		t.Fatal("missing expected file must set IndependentOK=false")
	}
}

func TestRestoreIndependentOKFalseWithoutSidecar(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s := &Service{
		Engine:      &restoreEngine{result: domain.RestoreResult{State: domain.RestoreVerifying, VerifiedByEngine: true}},
		Journal:     j,
		ManifestDir: t.TempDir(),
	}
	res, err := s.Restore(context.Background(), restoreReq(t, "ws-restore-noside", strings.Repeat("d", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.RestoreReady {
		t.Fatalf("got %s", res.State)
	}
	if res.IndependentOK {
		t.Fatal("missing sidecar must set IndependentOK=false")
	}
}
