package restic

import (
	"context"
	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"strings"
	"testing"
)

func reviewAdapter(result *ports.ProcessResult) *Adapter {
	return &Adapter{Runner: &process.Fake{Result: result}, Secrets: secrets.StaticProvider{Values: map[string]string{"pw": "review-password"}}, Connector: storage.Connector{}, Pin: domain.BinaryPin{Path: `C:\pinned\restic.exe`, SHA256: "fake"}}
}
func TestReviewTimedOutRestoreAndCheckNeverGreen(t *testing.T) {
	a := reviewAdapter(&ports.ProcessResult{ExitCode: 0, TimedOut: true})
	repo := storage.LocalDescriptor(t.TempDir(), "g", "d")
	pw := domain.SecretRef{Locator: "pw"}
	r, _ := a.Restore(context.Background(), domain.RestoreRequest{Repository: repo, PasswordRef: pw, SnapshotID: domain.SnapshotID(strings.Repeat("a", 64)), StagingRoot: t.TempDir(), DestinationDir: t.TempDir(), VerifyContent: true})
	if r.State == domain.RestoreVerifying || r.State == domain.RestoreReady {
		t.Error("timed out restore green")
	}
	c, _ := a.Check(context.Background(), repo, pw, true)
	if c.OK {
		t.Error("timed out check green")
	}
}
