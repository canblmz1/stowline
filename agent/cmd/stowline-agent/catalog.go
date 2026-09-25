package main

import (
	"context"
	"fmt"

	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const catalogUploadBatchSize = 500

type catalogEngine interface {
	ListSnapshotFull(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, snapshotID string) ([]ports.SnapshotEntry, error)
}

type catalogClient interface {
	CreateCatalog(ctx context.Context, snapshotID string, declared map[string]any) (string, string, error)
	UploadCatalogEntries(ctx context.Context, catalogID string, entries []map[string]any) error
	FinalizeCatalog(ctx context.Context, catalogID string, declared map[string]any) (bool, error)
	FailCatalog(ctx context.Context, catalogID, errorClass string) error
}

// buildCatalogFunc returns the one function both the automatic post-backup
// path (control.Executor.BuildCatalog, driven by the durable outbox) and
// the BUILD_SNAPSHOT_CATALOG command dispatch call. It never swallows
// anything itself: a fatal enumeration failure is reported via FailCatalog
// AND returned (wrapped in domain.ErrCatalogEnumerationFailed) every time.
// Deciding whether to swallow that error instead (the automatic path's
// requirement, so a permanently broken snapshot doesn't retry forever) is
// entirely the calling code's job -- see control.Executor.flushCatalogEvent.
//
// CreateCatalog is called before enumeration, with placeholder-zero
// declared totals, specifically so a catalog_id exists to call FailCatalog
// against even if enumeration itself fails; finalize is the only place
// real totals are ever verified (server-side), so a placeholder create has
// no correctness cost.
func buildCatalogFunc(eng catalogEngine, client catalogClient, repo domain.RepositoryDescriptor, password domain.SecretRef) func(context.Context, string) error {
	return func(ctx context.Context, snapshotID string) error {
		placeholder := map[string]any{"declared_file_count": 0, "declared_directory_count": 0, "declared_logical_bytes": 0, "declared_checksum": ""}
		catalogID, state, err := client.CreateCatalog(ctx, snapshotID, placeholder)
		if err != nil {
			return err
		}
		if state == "READY" {
			return nil // idempotent create found an already-finalized catalog -- nothing left to do
		}
		nodes, err := eng.ListSnapshotFull(ctx, repo, password, snapshotID)
		if err != nil {
			_ = client.FailCatalog(ctx, catalogID, "ENUMERATE_FAILED")
			return fmt.Errorf("%w: %v", domain.ErrCatalogEnumerationFailed, err)
		}
		var files, dirs int
		hashes := make([]string, 0, len(nodes))
		entries := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			if n.Type == "file" {
				files++
			} else {
				dirs++
			}
			h := restic.PathHash(n.Path)
			hashes = append(hashes, h)
			entries = append(entries, map[string]any{"path": n.Path, "parent_path": parentOf(n.Path), "name": n.Name, "type": n.Type, "size": n.Size, "mtime": n.MTime})
		}
		checksum := restic.CatalogChecksum(hashes)
		for start := 0; start < len(entries); start += catalogUploadBatchSize {
			end := start + catalogUploadBatchSize
			if end > len(entries) {
				end = len(entries)
			}
			if err := client.UploadCatalogEntries(ctx, catalogID, entries[start:end]); err != nil {
				return err
			}
		}
		finalizeDeclared := map[string]any{"file_count": files, "directory_count": dirs, "logical_bytes": totalSize(nodes), "checksum": checksum}
		ok, err := client.FinalizeCatalog(ctx, catalogID, finalizeDeclared)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("catalog %s: finalize reported mismatch", catalogID)
		}
		return nil
	}
}

func parentOf(path string) string {
	i := lastSlash(path)
	if i <= 0 {
		return ""
	}
	return path[:i]
}

func lastSlash(path string) int {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return i
		}
	}
	return -1
}

func totalSize(nodes []ports.SnapshotEntry) int64 {
	var sum int64
	for _, n := range nodes {
		if n.Type == "file" {
			sum += n.Size
		}
	}
	return sum
}
