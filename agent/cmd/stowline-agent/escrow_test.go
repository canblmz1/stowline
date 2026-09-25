package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

type fakeEscrowClient struct {
	kind, secret string
	calls        int
	err          error
}

func (f *fakeEscrowClient) PutEscrow(ctx context.Context, kind, secret string) error {
	f.calls++
	f.kind, f.secret = kind, secret
	return f.err
}

func TestPushRepositoryPasswordToEscrowSendsTheExactLocalSecret(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "restic-password.plain")
	if err := os.WriteFile(loc, []byte("my-repo-password"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := domain.SecretRef{Purpose: domain.SecretResticPassword, Provider: domain.SecretProviderFile, Locator: loc}
	client := &fakeEscrowClient{}
	if err := pushRepositoryPasswordToEscrow(context.Background(), client, ref); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one PutEscrow call, got %d", client.calls)
	}
	if client.kind != "restic-password" {
		t.Fatalf("kind = %s, want restic-password", client.kind)
	}
	if client.secret != "my-repo-password" {
		t.Fatalf("secret = %q, want the exact local file content", client.secret)
	}
}

func TestPushRepositoryPasswordToEscrowIsANoOpWithNoLocalPassword(t *testing.T) {
	client := &fakeEscrowClient{}
	if err := pushRepositoryPasswordToEscrow(context.Background(), client, domain.SecretRef{}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("must not call PutEscrow when there is no local password configured, got %d calls", client.calls)
	}
}

func TestPushRepositoryPasswordToEscrowPropagatesAServerFailure(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "restic-password.plain")
	if err := os.WriteFile(loc, []byte("my-repo-password"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := domain.SecretRef{Purpose: domain.SecretResticPassword, Provider: domain.SecretProviderFile, Locator: loc}
	client := &fakeEscrowClient{err: errors.New("network blip")}
	if err := pushRepositoryPasswordToEscrow(context.Background(), client, ref); err == nil {
		t.Fatal("expected the server-side failure to propagate so the caller can log it")
	}
}
