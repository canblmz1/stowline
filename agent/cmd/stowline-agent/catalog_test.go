package main

import (
	"context"
	"errors"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

type fakeCatalogEngine struct {
	nodes []ports.SnapshotEntry
	err   error
}

func (f *fakeCatalogEngine) ListSnapshotFull(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, snapshotID string) ([]ports.SnapshotEntry, error) {
	return f.nodes, f.err
}

type fakeCatalogClient struct {
	createCalls   []map[string]any
	uploadBatches [][]map[string]any
	finalizeCalls []map[string]any
	failedCatalog string
	failedClass   string
	createState   string
}

func (f *fakeCatalogClient) CreateCatalog(ctx context.Context, snapshotID string, declared map[string]any) (string, string, error) {
	f.createCalls = append(f.createCalls, declared)
	state := f.createState
	if state == "" {
		state = "PENDING"
	}
	return "cat-1", state, nil
}

func (f *fakeCatalogClient) UploadCatalogEntries(ctx context.Context, catalogID string, entries []map[string]any) error {
	f.uploadBatches = append(f.uploadBatches, entries)
	return nil
}

func (f *fakeCatalogClient) FinalizeCatalog(ctx context.Context, catalogID string, declared map[string]any) (bool, error) {
	f.finalizeCalls = append(f.finalizeCalls, declared)
	return true, nil
}

func (f *fakeCatalogClient) FailCatalog(ctx context.Context, catalogID, errorClass string) error {
	f.failedCatalog = catalogID
	f.failedClass = errorClass
	return nil
}

func TestBuildCatalogFuncEnumeratesUploadsAndFinalizes(t *testing.T) {
	nodes := make([]ports.SnapshotEntry, 0, 1200)
	for i := 0; i < 1200; i++ {
		nodes = append(nodes, ports.SnapshotEntry{Name: "f", Path: "/C/f" + string(rune('a'+i%26)) + string(rune(i)), Type: "file", Size: 10})
	}
	eng := &fakeCatalogEngine{nodes: nodes}
	client := &fakeCatalogClient{}
	build := buildCatalogFunc(eng, client, domain.RepositoryDescriptor{}, domain.SecretRef{})

	if err := build(context.Background(), "snap-1"); err != nil {
		t.Fatal(err)
	}
	if len(client.createCalls) != 1 {
		t.Fatalf("expected exactly one CreateCatalog call, got %d", len(client.createCalls))
	}
	if len(client.uploadBatches) != 3 { // 1200 entries / 500 per batch = 3 batches
		t.Fatalf("expected 3 upload batches for 1200 entries, got %d", len(client.uploadBatches))
	}
	total := 0
	for _, b := range client.uploadBatches {
		total += len(b)
	}
	if total != 1200 {
		t.Fatalf("expected all 1200 entries uploaded, got %d", total)
	}
	if len(client.finalizeCalls) != 1 {
		t.Fatalf("expected exactly one FinalizeCatalog call, got %d", len(client.finalizeCalls))
	}
	fin := client.finalizeCalls[0]
	if fin["file_count"] != 1200 || fin["directory_count"] != 0 {
		t.Fatalf("finalize totals wrong: %+v", fin)
	}
	if client.failedCatalog != "" {
		t.Fatalf("must not call FailCatalog on a successful build, got %s", client.failedCatalog)
	}
}

func TestBuildCatalogFuncCreatesFirstSoAFailureHasARealCatalogIDToFailAgainst(t *testing.T) {
	eng := &fakeCatalogEngine{err: errors.New("repository auth failed")}
	client := &fakeCatalogClient{}
	build := buildCatalogFunc(eng, client, domain.RepositoryDescriptor{}, domain.SecretRef{})

	err := build(context.Background(), "snap-2")
	if err == nil || !errors.Is(err, domain.ErrCatalogEnumerationFailed) {
		t.Fatalf("expected a domain.ErrCatalogEnumerationFailed-wrapped error, got %v", err)
	}
	if len(client.createCalls) != 1 {
		t.Fatalf("expected CreateCatalog to run before enumeration, got %d calls", len(client.createCalls))
	}
	if client.failedCatalog != "cat-1" || client.failedClass != "ENUMERATE_FAILED" {
		t.Fatalf("expected FailCatalog(cat-1, ENUMERATE_FAILED), got catalog=%s class=%s", client.failedCatalog, client.failedClass)
	}
	if len(client.uploadBatches) != 0 || len(client.finalizeCalls) != 0 {
		t.Fatal("must never upload or finalize after a fatal enumeration failure")
	}
}

func TestBuildCatalogFuncIsANoOpWhenCreateFindsAnAlreadyReadyCatalog(t *testing.T) {
	eng := &fakeCatalogEngine{nodes: []ports.SnapshotEntry{{Name: "f", Path: "/C/f", Type: "file", Size: 1}}}
	client := &fakeCatalogClient{createState: "READY"}
	build := buildCatalogFunc(eng, client, domain.RepositoryDescriptor{}, domain.SecretRef{})

	if err := build(context.Background(), "snap-3"); err != nil {
		t.Fatal(err)
	}
	if len(client.uploadBatches) != 0 || len(client.finalizeCalls) != 0 {
		t.Fatal("an already-READY catalog must not be re-uploaded or re-finalized")
	}
}
