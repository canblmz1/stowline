package ports

import (
	"context"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// BackupEngine is the Stowline operation port. Provider details do not belong here.
type BackupEngine interface {
	Version(ctx context.Context) (string, error)
	Init(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) (repoID string, err error)
	Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error)
	Snapshots(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) ([]Snapshot, error)
	Check(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, readData bool) (CheckResult, error)
	Restore(ctx context.Context, req domain.RestoreRequest) (domain.RestoreResult, error)
	CatConfig(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) (repoID string, err error)
	ReadSnapshot(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, id domain.SnapshotID) (*Snapshot, error)
}

type Snapshot struct {
	ID       domain.SnapshotID
	Time     string
	Hostname string
	Paths    []string
	Tags     []string
	Parent   string
}

type CheckResult struct {
	OK       bool
	ReadData bool
	Output   string
	ExitCode int
}

// SnapshotEntry is the sanitized, UI-facing shape of one file or directory
// inside a snapshot listing -- never restic's raw `ls --json` node, which
// can carry uid/gid/mode/extended-attribute fields that must not reach the
// admin UI. Exactly one directory level below the requested prefix.
type SnapshotEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	MTime string `json:"mtime"`
}
