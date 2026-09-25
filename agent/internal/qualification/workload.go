package qualification

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const WorkloadDirName = ".stowline-qualification-workload"

const DefaultWorkloadBytes int64 = 8 << 20

// PilotSyntheticCorpus is the only source root the Windows qualifier may bind.
const PilotSyntheticCorpus = `C:\Stowline\TestCorpus`

// ExpandWorkload writes unique random bytes under root. It is qualification-only
// file generation, not a production sleep.
func ExpandWorkload(root, runID string, size int64) (string, error) {
	if err := domain.ValidateQualificationRunID(runID); err != nil {
		return "", err
	}
	if size <= 0 {
		return "", nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(absRoot, WorkloadDirName)
	if err := withinRoot(absRoot, dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, runID+".bin")
	if err := withinRoot(absRoot, dest); err != nil {
		return "", err
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 1024*1024)
	var written int64
	for written < size {
		n := len(buf)
		if size-written < int64(n) {
			n = int(size - written)
		}
		if _, err := rand.Read(buf[:n]); err != nil {
			return "", err
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return "", err
		}
		written += int64(n)
	}
	return dest, nil
}

func withinRoot(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: qualification workload escaped source root", domain.ErrRestorePathRejected)
	}
	return nil
}
