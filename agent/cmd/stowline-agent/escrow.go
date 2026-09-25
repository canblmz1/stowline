package main

import (
	"context"

	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

type escrowClient interface {
	PutEscrow(ctx context.Context, kind, secret string) error
}

// pushRepositoryPasswordToEscrow seals a copy of this machine's repository
// password into the control plane's vault so an admin can still reach it
// (Şifre Kasası) if this PC is lost, without the password ever having lived
// anywhere but this call's argument and the existing sealed server-side
// store. Best-effort: escrow is a convenience on top of a backup that
// already works locally, not a precondition for one -- any failure here is
// returned to the caller to log, never to block startup or backups.
func pushRepositoryPasswordToEscrow(ctx context.Context, client escrowClient, ref domain.SecretRef) error {
	if ref.Locator == "" {
		return nil
	}
	prov, _, err := secrets.ForRef(ref)
	if err != nil {
		return err
	}
	h, err := prov.Open(ctx, ref)
	if err != nil {
		return err
	}
	defer h.Close()
	secret := string(append([]byte(nil), h.Bytes()...))
	return client.PutEscrow(ctx, "restic-password", secret)
}
