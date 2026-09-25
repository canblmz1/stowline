package restic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestAdapterUsesRunnerNotScatteredExec(t *testing.T) {
	fake := &process.Fake{Result: &ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte(`{"message_type":"summary","snapshot_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","files_new":1}`),
		Stderr:   []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n"),
	}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": domain.CanarySecret}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\Stowline\bin\restic.exe`, SHA256: "abc", Version: "0.19.1"},
	}
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  storage.LocalDescriptor(`C:\Stowline\Repos\local`, "g1", "dev"),
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSRequired,
		PasswordRef: domain.SecretRef{Locator: "pw"},
		Host:        "dev",
	}
	// Fake runner skips digest verification because Adapter still asks Runner.Run;
	// Fake does not call verify. Check argv.
	_, _ = a.Backup(context.Background(), req)
	if len(fake.Calls) == 0 {
		t.Fatal("expected runner call")
	}
	spec := fake.Calls[0]
	for _, arg := range spec.Args {
		if strings.Contains(arg, domain.CanarySecret) {
			t.Fatal("secret in args")
		}
		if arg == "forget" || arg == "prune" {
			t.Fatal("maintenance")
		}
	}
	foundPW := false
	for _, e := range spec.Env {
		if e.Name == "RESTIC_PASSWORD" {
			foundPW = true
			if !e.Secret {
				t.Fatal("password env must be marked secret")
			}
		}
		if e.Name == "RESTIC_PASSWORD_COMMAND" {
			t.Fatal("password command")
		}
	}
	if !foundPW {
		t.Fatal("password should be in env")
	}
}

func TestUnknownExitFails(t *testing.T) {
	fake := &process.Fake{Result: &ports.ProcessResult{
		ExitCode: 77,
		Stdout:   []byte(`{"message_type":"summary","snapshot_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a"},
	}
	res, err := a.Backup(context.Background(), domain.BackupRequest{
		DeviceID:    "d",
		Repository:  storage.LocalDescriptor(`C:\r`, "g", "d"),
		SourceRoots: []string{`C:\s`},
		VSSMode:     domain.VSSDisabled,
		PasswordRef: domain.SecretRef{Locator: "pw"},
	})
	if err == nil && res.Outcome == domain.PhaseSucceeded {
		t.Fatal("unknown exit must not succeed")
	}
}

func TestTimeoutWithPublishedSnapshotIsNotSuccess(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fake := &process.Fake{Hook: func(spec ports.Spec) (*ports.ProcessResult, error) {
		joined := strings.Join(spec.Args, " ")
		if strings.Contains(joined, "backup") {
			return &ports.ProcessResult{
				ExitCode: 0,
				TimedOut: true,
				Stdout:   []byte(`{"message_type":"summary","snapshot_id":"` + id + `","files_new":1}`),
			}, nil
		}
		return &ports.ProcessResult{
			ExitCode: 0,
			Stdout:   []byte(`[{"id":"` + id + `","hostname":"d","paths":["C:\\s"],"tags":["attempt:x"]}]`),
		}, nil
	}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a"},
	}
	res, err := a.Backup(context.Background(), domain.BackupRequest{
		DeviceID:    "d",
		Repository:  storage.LocalDescriptor(`C:\r`, "g", "d"),
		SourceRoots: []string{`C:\s`},
		VSSMode:     domain.VSSDisabled,
		PasswordRef: domain.SecretRef{Locator: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == domain.PhaseSucceeded {
		t.Fatal("timeout after publish must not be SUCCEEDED")
	}
	if res.Outcome != domain.PhasePartial {
		t.Fatalf("got %s", res.Outcome)
	}
	if !res.PublishedNotGreen {
		t.Fatal("must mark published-not-green")
	}
}

const testSnapID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func vssBackupFake(backup *ports.ProcessResult) *process.Fake {
	return &process.Fake{Hook: func(spec ports.Spec) (*ports.ProcessResult, error) {
		args := strings.Join(spec.Args, " ")
		switch {
		case strings.Contains(args, "backup"):
			return backup, nil
		case strings.Contains(args, "version"):
			return &ports.ProcessResult{ExitCode: 0, Stdout: []byte("restic 0.19.1 compiled with go1.26.4 on windows/amd64\n")}, nil
		case strings.Contains(args, "snapshots"), strings.Contains(args, "cat"):
			return &ports.ProcessResult{
				ExitCode: 0,
				Stdout:   []byte(`[{"id":"` + testSnapID + `","hostname":"dev","paths":["C:\\Stowline\\TestCorpus"],"tags":["stowline"]}]`),
			}, nil
		default:
			return &ports.ProcessResult{ExitCode: 0}, nil
		}
	}}
}

func vssBackupRequest() domain.BackupRequest {
	return domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  storage.LocalDescriptor(`C:\Stowline\Repos\local`, "g1", "dev"),
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSRequired,
		PasswordRef: domain.SecretRef{Locator: "pw"},
		Host:        "dev",
	}
}

func TestVSSRequiredBackupOmitsJSON(t *testing.T) {
	fake := vssBackupFake(&ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nsnapshot aaaaaaaa saved\n"),
	})
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a", Version: "0.19.1"},
	}
	_, _ = a.Backup(context.Background(), vssBackupRequest())
	if len(fake.Calls) == 0 {
		t.Fatal("expected runner call")
	}
	var backupArgs []string
	for _, c := range fake.Calls {
		if contains(c.Args, "backup") {
			backupArgs = c.Args
			break
		}
	}
	if len(backupArgs) == 0 {
		t.Fatal("backup was not invoked")
	}
	if contains(backupArgs, "--json") {
		t.Fatal("VSS-required backup must omit --json")
	}
	if !contains(backupArgs, "--use-fs-snapshot") {
		t.Fatal("VSS-required backup must pass --use-fs-snapshot")
	}
}

func TestVSSRequiredStdoutEvidenceSucceeds(t *testing.T) {
	fake := vssBackupFake(&ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nsnapshot aaaaaaaa saved\n"),
	})
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a", Version: "0.19.1"},
	}
	res, err := a.Backup(context.Background(), vssBackupRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.PhaseSucceeded {
		t.Fatalf("outcome=%s class=%s msg=%s vss=%+v", res.Outcome, res.ErrorClass, res.ErrorMessage, res.VSS)
	}
	if res.Consistency != domain.ConsistencyVerified || !res.VSS.PositiveAllRequired {
		t.Fatalf("consistency=%s positive=%v", res.Consistency, res.VSS.PositiveAllRequired)
	}
	if res.PublishedNotGreen {
		t.Fatal("published_not_green must be false")
	}
	if string(res.SnapshotID) != testSnapID {
		t.Fatalf("snapshot id %q", res.SnapshotID)
	}
}

func TestVSSRequiredStderrEvidenceSucceeds(t *testing.T) {
	fake := vssBackupFake(&ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte("snapshot aaaaaaaa saved\n"),
		Stderr:   []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n"),
	})
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a", Version: "0.19.1"},
	}
	res, err := a.Backup(context.Background(), vssBackupRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.PhaseSucceeded || res.Consistency != domain.ConsistencyVerified {
		t.Fatalf("outcome=%s consistency=%s", res.Outcome, res.Consistency)
	}
}

func TestVSSRequiredTruncatedNotVerified(t *testing.T) {
	fake := vssBackupFake(&ports.ProcessResult{
		ExitCode:        0,
		StdoutTruncated: true,
		Stdout:          []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nsnapshot aaaaaaaa saved\n"),
	})
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a", Version: "0.19.1"},
	}
	res, err := a.Backup(context.Background(), vssBackupRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == domain.PhaseSucceeded || res.Consistency == domain.ConsistencyVerified {
		t.Fatalf("truncated VSS capture must not be SUCCEEDED/VERIFIED: %+v", res)
	}
	if res.Outcome != domain.PhasePartial || !res.PublishedNotGreen {
		t.Fatalf("want PARTIAL published-not-green, got %s green=%v", res.Outcome, res.PublishedNotGreen)
	}
}

func TestVSSRequiredSilenceIsPartial(t *testing.T) {
	fake := vssBackupFake(&ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte("snapshot aaaaaaaa saved\n"),
	})
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a", Version: "0.19.1"},
	}
	res, err := a.Backup(context.Background(), vssBackupRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == domain.PhaseSucceeded || res.Consistency == domain.ConsistencyVerified {
		t.Fatal("exit 0 without VSS lines must not be VERIFIED")
	}
}

func TestCatConfigReadOnlyAndNoSecretOnArgv(t *testing.T) {
	id := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fake := &process.Fake{Result: &ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte(`{"id":"` + id + `","version":2}`),
	}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": domain.CanarySecret}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\Stowline\bin\restic.exe`, SHA256: "abc"},
	}
	got, err := a.CatConfig(context.Background(), storage.LocalDescriptor(`C:\Stowline\Repos\local`, "g1", "dev"), domain.SecretRef{Locator: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("id %s", got)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls %d", len(fake.Calls))
	}
	spec := fake.Calls[0]
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "cat") || !strings.Contains(joined, "config") {
		t.Fatalf("expected restic cat config, got %v", spec.Args)
	}
	if !contains(spec.Args, "--no-lock") {
		t.Fatalf("cat config must not take a repository lock: %v", spec.Args)
	}
	for _, bad := range []string{"backup", "init", "forget", "prune", "key", "unlock", "repair"} {
		if contains(spec.Args, bad) {
			t.Fatalf("read-only cat config must not include %s", bad)
		}
	}
	for _, arg := range spec.Args {
		if strings.Contains(arg, domain.CanarySecret) || strings.HasPrefix(strings.ToLower(arg), "--password") {
			t.Fatalf("secret on argv: %q", arg)
		}
	}
	foundPW := false
	for _, e := range spec.Env {
		if e.Name == "RESTIC_PASSWORD" {
			foundPW = true
			if !e.Secret {
				t.Fatal("password env must be marked secret")
			}
			if e.Value != domain.CanarySecret {
				t.Fatal("test fixture password not injected")
			}
		}
	}
	if !foundPW {
		t.Fatal("password must be in env, not argv")
	}
}

func TestCatConfigWrongPasswordIsAuth(t *testing.T) {
	fake := &process.Fake{Result: &ports.ProcessResult{ExitCode: ExitWrongPassword, Stderr: []byte("wrong password")}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": domain.CanarySecret}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a"},
	}
	_, err := a.CatConfig(context.Background(), storage.LocalDescriptor(`C:\r`, "g", "d"), domain.SecretRef{Locator: "pw"})
	if err == nil {
		t.Fatal("wrong password must fail")
	}
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("want AUTH, got %v", err)
	}
}

func TestBackupWrongPasswordIsAuthNotSuccess(t *testing.T) {
	fake := &process.Fake{Result: &ports.ProcessResult{ExitCode: ExitWrongPassword, Stderr: []byte("wrong password")}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\x\restic.exe`, SHA256: "a"},
	}
	res, err := a.Backup(context.Background(), domain.BackupRequest{
		DeviceID:    "d",
		Repository:  storage.LocalDescriptor(`C:\r`, "g", "d"),
		SourceRoots: []string{`C:\s`},
		VSSMode:     domain.VSSDisabled,
		PasswordRef: domain.SecretRef{Locator: "pw"},
	})
	if err != nil && res.Outcome == domain.PhaseSucceeded {
		t.Fatal("wrong password must not be SUCCESS")
	}
	if res.Outcome != domain.PhaseFailed || res.ErrorClass != domain.ErrorAuth {
		t.Fatalf("want FAILED AUTH, got %s %s err=%v", res.Outcome, res.ErrorClass, err)
	}
}
