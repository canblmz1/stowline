package main

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// This pins down agent/cmd/stowline-agent/main.go:452's
// `progress = &liveBackupProgress{present: true}` -- constructed with
// present already true, before restic has produced a single real sample.
// The control plane's operator UI derives its "PREPARING" vs "BACKING_UP"
// phase, and its "old agent doesn't report %" fallback message, entirely
// from whether this zero-value placeholder (vs. no data at all) has ever
// crossed the wire. If a future refactor changes this construction back to
// `present: false` or drops the field, that design breaks silently without
// this test.
func TestLiveBackupProgressSendsAZeroValuePlaceholderBeforeAnyRealSample(t *testing.T) {
	p := &liveBackupProgress{present: true}
	got := p.snapshot()
	if got == nil {
		t.Fatal("snapshot() returned nil; a granted backup lease must report a real (zero-value) progress from tick one, not silence")
	}
	if *got != (domain.BackupProgress{}) {
		t.Fatalf("expected an all-zero placeholder before any restic output, got %#v", *got)
	}
}

func TestLiveBackupProgressReturnsNilForANonBackupLease(t *testing.T) {
	var p *liveBackupProgress // e.g. a RESTORE lease, which never tracks backup progress
	if got := p.snapshot(); got != nil {
		t.Fatalf("a nil *liveBackupProgress must keep reporting nil, got %#v", got)
	}
}

func TestLiveBackupProgressResetForNewRunDiscardsThePreviousRunsValue(t *testing.T) {
	p := &liveBackupProgress{}
	p.update(domain.BackupProgress{PercentDone: 87.5, BytesDone: 900})
	p.resetForNewRun()
	got := p.snapshot()
	if got == nil {
		t.Fatal("resetForNewRun must leave the shared instance present (a new run just started)")
	}
	if *got != (domain.BackupProgress{}) {
		t.Fatalf("expected the previous run's value discarded, got %#v", *got)
	}
}

func TestLiveBackupProgressClearReportsIdleAfterABackupFinishes(t *testing.T) {
	p := &liveBackupProgress{}
	p.update(domain.BackupProgress{PercentDone: 100, BytesDone: 1000})
	p.clear()
	if got := p.snapshot(); got != nil {
		t.Fatalf("clear must make the shared instance report nil (idle), got %#v", got)
	}
}
