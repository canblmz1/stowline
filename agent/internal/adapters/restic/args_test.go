package restic

import (
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestBackupArgsQualificationTagsAreBounded(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:           "dev",
		Repository:         domain.RepositoryDescriptor{BackendKind: domain.BackendLocal, Location: `C:\repo`, Capabilities: domain.DefaultCapabilities(domain.BackendLocal)},
		SourceRoots:        []string{`C:\Stowline\TestCorpus`},
		VSSMode:            domain.VSSRequired,
		Host:               "dev",
		AttemptClass:       domain.AttemptQualification,
		QualificationRunID: "qualrun-001",
		JobID:              domain.QualificationJobID("qualrun-001"),
		AttemptID:          domain.QualificationAttemptID("qualrun-001"),
		SlotKey:            domain.QualificationSlotKey("qualrun-001"),
	}
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(extra, " ")
	if !strings.Contains(joined, "class:QUALIFICATION_BACKUP") || !strings.Contains(joined, "qual:qualrun-001") {
		t.Fatalf("%s", joined)
	}
	for _, a := range extra {
		if strings.HasPrefix(a, "--password") || a == "--verbose" {
			t.Fatalf("forbidden %q", a)
		}
	}
}

func TestBackupArgsNeverTreatFilenameAsFlag(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  domain.RepositoryDescriptor{BackendKind: domain.BackendLocal, Location: `C:\repo`, Capabilities: domain.DefaultCapabilities(domain.BackendLocal)},
		SourceRoots: []string{`C:\Stowline\TestCorpus`, `--exclude`, `-r`},
		VSSMode:     domain.VSSDisabled,
		Host:        "dev",
	}
	_, err := backupExtras(req)
	if err == nil {
		t.Fatal("expected rejection of flag-like source root")
	}
}

func TestBackupArgsUseDashDashBeforePaths(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  domain.RepositoryDescriptor{BackendKind: domain.BackendLocal, Location: `C:\repo`, Capabilities: domain.DefaultCapabilities(domain.BackendLocal)},
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSRequired,
		Host:        "dev",
		JobID:       "job1",
		AttemptID:   "att1",
	}
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(extra, " ")
	if !strings.Contains(joined, "--use-fs-snapshot") {
		t.Fatal("VSS required must pass --use-fs-snapshot")
	}
	dd := indexOf(extra, "--")
	if dd < 0 {
		t.Fatal("missing -- before paths")
	}
	if extra[dd+1] != `C:\Stowline\TestCorpus` {
		t.Fatalf("path after --: %q", extra[dd+1])
	}
	args, err := buildArgs(cmdBackup, ports.BoundRepository{Location: `C:\repo`}, extra...)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.HasPrefix(strings.ToLower(a), "--password") {
			t.Fatal("password flag present")
		}
	}
	if contains(args, "forget") || contains(args, "prune") {
		t.Fatal("maintenance command leaked")
	}
	if !contains(args, "--use-fs-snapshot") {
		t.Fatal("VSS required argv must include --use-fs-snapshot")
	}
	if contains(args, "--json") {
		t.Fatal("VSS required argv must omit --json so restic 0.19.1 emits VSS printer.P lines")
	}
	for _, a := range args {
		if a == "--verbose" || strings.HasPrefix(a, "--verbose=") {
			t.Fatalf("VSS required argv must not enable restic verbose file dumps: %q", a)
		}
	}
}

func TestBackupArgsVSSDisabledKeepsJSON(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  domain.RepositoryDescriptor{BackendKind: domain.BackendLocal, Location: `C:\repo`, Capabilities: domain.DefaultCapabilities(domain.BackendLocal)},
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSDisabled,
		Host:        "dev",
	}
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	if contains(extra, "--use-fs-snapshot") {
		t.Fatal("VSS disabled must not pass --use-fs-snapshot")
	}
	args, err := buildArgs(cmdBackup, ports.BoundRepository{Location: `C:\repo`}, extra...)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(args, "--json") {
		t.Fatal("non-VSS backup must keep --json")
	}
}

func TestWANBackupRequiresCompiledUploadLimit(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  domain.RepositoryDescriptor{BackendKind: domain.BackendRESTGateway, Location: "rest:https://gw/restic/d/g", Capabilities: domain.DefaultCapabilities(domain.BackendRESTGateway)},
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSDisabled,
		Host:        "dev",
	}
	if _, err := backupExtras(req); err == nil {
		t.Fatal("REST backup with 0 KiB/s must fail closed")
	}
	req.BandwidthKiBps = 439
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(extra, "--limit-upload") || !contains(extra, "439") {
		t.Fatalf("missing compiled ceiling: %v", extra)
	}
	assertNoRetries(t, extra)
}

func TestRestic0191BackupArgvOmitsRetries(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:       "dev",
		Repository:     domain.RepositoryDescriptor{BackendKind: domain.BackendRESTGateway, Location: "rest:https://gw/restic/d/g", Capabilities: domain.DefaultCapabilities(domain.BackendRESTGateway)},
		SourceRoots:    []string{`C:\Stowline\TestCorpus`},
		VSSMode:        domain.VSSRequired,
		Host:           "dev",
		BandwidthKiBps: 439,
	}
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	args, err := buildArgs(cmdBackup, ports.BoundRepository{Location: req.Repository.Location}, extra...)
	if err != nil {
		t.Fatal(err)
	}
	assertNoRetries(t, args)
	if !contains(args, "--repo") || !contains(args, req.Repository.Location) {
		t.Fatalf("missing --repo: %v", args)
	}
	if !contains(args, "--use-fs-snapshot") {
		t.Fatal("VSS required argv must include --use-fs-snapshot")
	}
	if !contains(args, "--limit-upload") || !contains(args, "439") {
		t.Fatalf("missing compiled ceiling: %v", args)
	}
}

func TestRestic0191RestoreArgvOmitsRetries(t *testing.T) {
	req := domain.RestoreRequest{
		SnapshotID:           domain.SnapshotID(strings.Repeat("a", 64)),
		DestinationDir:       `C:\Stowline\Restore\job`,
		Repository:           domain.RepositoryDescriptor{BackendKind: domain.BackendRESTGateway, Location: "rest:https://gw/restic/d/g", Capabilities: domain.DefaultCapabilities(domain.BackendRESTGateway)},
		RestoreDownloadKiBps: 439,
	}
	extra, err := restoreExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	args, err := buildArgs(cmdRestore, ports.BoundRepository{Location: req.Repository.Location}, extra...)
	if err != nil {
		t.Fatal(err)
	}
	assertNoRetries(t, args)
	if !contains(args, "--limit-download") || !contains(args, "439") {
		t.Fatalf("missing compiled download ceiling: %v", args)
	}
}

func TestRestoreSingleFileUsesOneExactInclude(t *testing.T) {
	selected := "/C/Users/Lenovo/Desktop/example.txt"
	req := domain.RestoreRequest{
		SnapshotID:           domain.SnapshotID(strings.Repeat("a", 64)),
		DestinationDir:       `C:\Stowline\Restore\job`,
		Selections:           []string{selected},
		RestoreDownloadKiBps: 439,
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendRESTGateway,
			Location:     "rest:https://gw/restic/d/g",
			Capabilities: domain.DefaultCapabilities(domain.BackendRESTGateway),
		},
	}
	extra, err := restoreExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	includes := 0
	for i := range extra {
		if extra[i] == "--include" {
			includes++
			if i+1 >= len(extra) || extra[i+1] != selected {
				t.Fatalf("include argv = %v", extra)
			}
		}
	}
	if includes != 1 {
		t.Fatalf("want one exact include, argv = %v", extra)
	}
}

func TestRestic0191RejectsRetriesFlag(t *testing.T) {
	if err := validateArgv([]string{"backup", "--retries", "2"}); err == nil {
		t.Fatal("restic 0.19.1 --retries must be rejected")
	}
}

func TestLocalLabMayOmitUploadLimit(t *testing.T) {
	req := domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  domain.RepositoryDescriptor{BackendKind: domain.BackendLocal, Location: `C:\repo`, Capabilities: domain.DefaultCapabilities(domain.BackendLocal)},
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSDisabled,
		Host:        "dev",
	}
	extra, err := backupExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	if contains(extra, "--limit-upload") {
		t.Fatal("LOCAL lab unlimited must omit --limit-upload")
	}
}

func TestWANRestoreRequiresDownloadLimit(t *testing.T) {
	req := domain.RestoreRequest{
		SnapshotID:     domain.SnapshotID(strings.Repeat("a", 64)),
		DestinationDir: `C:\Stowline\Restore\job`,
		Repository:     domain.RepositoryDescriptor{BackendKind: domain.BackendRESTGateway, Location: "rest:https://gw/restic/d/g", Capabilities: domain.DefaultCapabilities(domain.BackendRESTGateway)},
	}
	if _, err := restoreExtras(req); err == nil {
		t.Fatal("WAN restore without download ceiling must fail")
	}
	req.RestoreDownloadKiBps = 122
	extra, err := restoreExtras(req)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(extra, "--limit-download") || !contains(extra, "122") {
		t.Fatalf("%v", extra)
	}
	assertNoRetries(t, extra)
}

func TestForbiddenCommandsRejected(t *testing.T) {
	for _, c := range []string{"forget", "prune", "unlock", "key"} {
		_, err := buildArgs(commandKind(c), ports.BoundRepository{Location: "x"})
		if err == nil {
			t.Fatalf("expected forbid %s", c)
		}
	}
}

func TestNoPasswordCommandOption(t *testing.T) {
	err := validateArgv([]string{"backup", "--password-command", "whoami"})
	if err == nil {
		t.Fatal("expected rejection")
	}
}

func TestRcloneOptionAllowlist(t *testing.T) {
	// The one accepted shape: unquoted absolute forward-slash path to rclone.
	// Restic parses -o as CSV (rejects "), then SplitShellStrings (splits on \
	// and spaces); a forward-slash path with no spaces survives both and forces
	// Go's exec to run that exact file with no PATH/CWD lookup.
	if err := validateOption(ports.KV{Key: "rclone.program", Value: `C:/Stowline/bin/rclone.exe`}); err != nil {
		t.Fatalf("unquoted absolute forward-slash path must be accepted: %v", err)
	}
	for _, bad := range []string{
		`rclone.exe`,                       // bare basename -> PATH/CWD lookup
		`C:\Stowline\bin\rclone.exe`,       // backslash -> exec: "C:" (DEV-005)
		`"C:/Stowline/bin/rclone.exe"`,     // quoted -> restic CSV "-o" parse error
		`C:/Stowline/bin/rclone.exe --x`,   // trailing injected argument (space)
		`C:/Stowline/bin/rclone.exe,calc`,  // CSV delimiter
		`C:/Stowline/bin/notrclone.exe`,    // not the rclone executable
		`C:/Stowline/../evil/rclone.exe`,   // traversal
		`rclone.exe`,                       // not absolute
		`//host/share/rclone.exe`,          // UNC-ish
		`C:/Stowline/bin/rclone.exe;calc`,  // shell metacharacter
		`C:/Stowline Pilot/bin/rclone.exe`, // space in path
	} {
		if err := validateOption(ports.KV{Key: "rclone.program", Value: bad}); err == nil {
			t.Fatalf("rclone.program %q must be rejected", bad)
		}
	}
	if err := validateOption(ports.KV{Key: "rclone.args", Value: "--rc"}); err == nil {
		t.Fatal("rc must be rejected")
	}
}

func indexOf(ss []string, w string) int {
	for i, s := range ss {
		if s == w {
			return i
		}
	}
	return -1
}

func contains(ss []string, w string) bool {
	return indexOf(ss, w) >= 0
}

func assertNoRetries(t *testing.T, args []string) {
	t.Helper()
	for _, a := range args {
		if a == "--retries" || strings.HasPrefix(a, "--retries=") {
			t.Fatalf("restic 0.19.1 argv must not contain --retries: %v", args)
		}
	}
}
