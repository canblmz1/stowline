package restic

import (
	"fmt"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const (
	PinnedVersion    = "0.19.1"
	VSSParserBoundTo = "0.19.1"
)

var allowedCommands = map[string]struct{}{
	"version":   {},
	"init":      {},
	"backup":    {},
	"snapshots": {},
	"ls":        {},
	"restore":   {},
	"check":     {},
	"cat":       {},
}

var forbiddenCommands = []string{
	"forget", "prune", "repair", "unlock", "key", "rewrite",
	"self-update", "copy", "migrate", "mount", "dump", "recover",
	"options", "tag", "cache", "init-debug",
}

type commandKind string

const (
	cmdVersion   commandKind = "version"
	cmdInit      commandKind = "init"
	cmdBackup    commandKind = "backup"
	cmdSnapshots commandKind = "snapshots"
	cmdLS        commandKind = "ls"
	cmdRestore   commandKind = "restore"
	cmdCheck     commandKind = "check"
	cmdCatConfig commandKind = "cat-config"
	cmdCatSnap   commandKind = "cat-snapshot"
)

type invocation struct {
	Kind    commandKind
	Args    []string
	Options []ports.KV
}

func globalJSONFlags() []string {
	return []string{"--json", "--no-lock=false"}
}

func buildArgs(kind commandKind, bound ports.BoundRepository, extra ...string) ([]string, error) {
	if err := rejectExtra(extra); err != nil {
		return nil, err
	}
	var args []string
	if useJSON(kind, extra) {
		args = append(args, "--json")
	}
	if bound.Location != "" && kind != cmdVersion {
		args = append(args, "--repo", bound.Location)
	}
	for _, o := range bound.Options {
		if err := validateOption(o); err != nil {
			return nil, err
		}
		args = append(args, "-o", o.Key+"="+o.Value)
	}
	switch kind {
	case cmdVersion:
		args = []string{"version"}
	case cmdInit:
		args = append(args, "init")
	case cmdBackup:
		args = append(args, "backup")
		args = append(args, extra...)
	case cmdSnapshots:
		args = append(args, "snapshots")
		args = append(args, extra...)
	case cmdLS:
		args = append(args, "ls")
		args = append(args, extra...)
	case cmdRestore:
		args = append(args, "restore")
		args = append(args, extra...)
	case cmdCheck:
		args = append(args, "check")
		args = append(args, extra...)
	case cmdCatConfig:
		// Reading the config needs no lock; skipping it saves three slow
		// Drive round trips (lock list, create, delete) per password check,
		// and setup checks the password three times in a row.
		args = append(args, "--no-lock", "cat", "config")
	case cmdCatSnap:
		args = append(args, "cat", "snapshot")
		args = append(args, extra...)
	default:
		return nil, domain.ErrForbiddenCommand
	}
	if err := validateArgv(args); err != nil {
		return nil, err
	}
	return args, nil
}

// useJSON reports whether this restic invocation should pass --json.
//
// Restic 0.19.1 cmd_backup.go wires VSS creating/success through msgMessage →
// printer.P, but only when !gopts.JSON. NewProgressPrinter also forces
// verbosity=0 under --json, so printer.P is silent. Those messages are the
// only positive VSS evidence Stowline accepts. VSS-required backups therefore
// omit --json. Other commands keep --json.
//
// Do not add --verbose/--verbose=2: printer.P already emits VSS lines at the
// default verbosity (>=1). --verbose=2 enables printer.VV per-file dumps.
func useJSON(kind commandKind, extra []string) bool {
	if kind == cmdVersion {
		return false
	}
	if kind == cmdBackup && hasArg(extra, "--use-fs-snapshot") {
		return false
	}
	return true
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func backupExtras(req domain.BackupRequest) ([]string, error) {
	var extra []string
	if req.VSSMode == domain.VSSRequired {
		extra = append(extra, "--use-fs-snapshot")
	}
	host := req.Host
	if host == "" {
		host = req.DeviceID
	}
	if host != "" {
		if looksLikeFlag(host) {
			return nil, domain.ErrArbitraryFlags
		}
		extra = append(extra, "--host", host)
	}
	extra = append(extra, "--tag", "stowline")
	if req.JobID != "" {
		extra = append(extra, "--tag", "job:"+string(req.JobID))
	}
	if req.AttemptID != "" {
		extra = append(extra, "--tag", "attempt:"+string(req.AttemptID))
	}
	if req.AttemptClass == domain.AttemptQualification {
		extra = append(extra, "--tag", "class:QUALIFICATION_BACKUP")
		if req.QualificationRunID != "" {
			extra = append(extra, "--tag", "qual:"+req.QualificationRunID)
		}
	}
	if err := applyUploadLimit(&extra, req.Repository.BackendKind, req.BandwidthKiBps); err != nil {
		return nil, err
	}
	// Restic 0.19.1 has no --retries CLI flag. Retry budget is the scheduler
	// (one daily slot, max 3 attempts) plus gateway/provider circuit breakers.
	for _, ex := range req.Excludes {
		if looksLikeFlag(ex) || strings.ContainsAny(ex, "\n\r") {
			return nil, domain.ErrArbitraryFlags
		}
		extra = append(extra, "--exclude", ex)
	}
	extra = append(extra, "--")
	for _, root := range req.SourceRoots {
		if looksLikeFlag(root) || root == "" || strings.ContainsAny(root, "\n\r") {
			return nil, domain.ErrArbitraryFlags
		}
		extra = append(extra, root)
	}
	return extra, nil
}

func applyUploadLimit(extra *[]string, kind domain.BackendKind, kibps int) error {
	if domain.RequiresWANRateLimit(kind) {
		if err := domain.ValidateResticKiBps(kibps); err != nil {
			return err
		}
		*extra = append(*extra, "--limit-upload", fmt.Sprintf("%d", kibps))
		return nil
	}
	if kibps < 0 || kibps > domain.MaxResticKiBps {
		return fmt.Errorf("%w: bandwidth", domain.ErrPolicyInvalid)
	}
	if kibps > 0 {
		*extra = append(*extra, "--limit-upload", fmt.Sprintf("%d", kibps))
	}
	return nil
}

func applyDownloadLimit(extra *[]string, kind domain.BackendKind, kibps int) error {
	if domain.RequiresWANRateLimit(kind) {
		if err := domain.ValidateResticKiBps(kibps); err != nil {
			return err
		}
		*extra = append(*extra, "--limit-download", fmt.Sprintf("%d", kibps))
		return nil
	}
	if kibps < 0 || kibps > domain.MaxResticKiBps {
		return fmt.Errorf("%w: bandwidth", domain.ErrPolicyInvalid)
	}
	if kibps > 0 {
		*extra = append(*extra, "--limit-download", fmt.Sprintf("%d", kibps))
	}
	return nil
}

func restoreExtras(req domain.RestoreRequest) ([]string, error) {
	if len(req.SnapshotID) != 64 {
		return nil, fmt.Errorf("%w: snapshot id", domain.ErrArbitraryFlags)
	}
	var extra []string
	extra = append(extra, string(req.SnapshotID))
	extra = append(extra, "--target", req.DestinationDir)
	if req.VerifyContent {
		extra = append(extra, "--verify")
	}
	limit := req.RestoreDownloadKiBps
	if limit == 0 {
		limit = req.BandwidthKiBps
	}
	if err := applyDownloadLimit(&extra, req.Repository.BackendKind, limit); err != nil {
		return nil, err
	}
	for _, sel := range req.Selections {
		if looksLikeFlag(sel) || strings.ContainsAny(sel, "\n\r") {
			return nil, domain.ErrArbitraryFlags
		}
		if sel == "" {
			continue
		}
		extra = append(extra, "--include", sel)
	}
	return extra, nil
}

func resticCommand(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if _, ok := allowedCommands[a]; ok {
			return a
		}
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func validateArgv(args []string) error {
	if len(args) == 0 {
		return domain.ErrForbiddenCommand
	}
	cmd := resticCommand(args)
	if _, ok := allowedCommands[cmd]; !ok {
		return fmt.Errorf("%w: %s", domain.ErrForbiddenCommand, cmd)
	}
	for _, a := range args {
		al := strings.ToLower(a)
		for _, f := range forbiddenCommands {
			if al == f {
				return fmt.Errorf("%w: %s", domain.ErrForbiddenCommand, f)
			}
		}
		if strings.HasPrefix(al, "--password") || al == "-p" {
			return domain.ErrSecretInArguments
		}
		if al == "--retries" || strings.HasPrefix(al, "--retries=") {
			return fmt.Errorf("%w: restic 0.19.1 has no --retries flag", domain.ErrArbitraryFlags)
		}
		if strings.Contains(al, "password-command") {
			return domain.ErrArbitraryFlags
		}
		if strings.Contains(a, "://") && strings.Contains(a, "@") {
			return domain.ErrSecretInArguments
		}
	}
	return nil
}

func validateOption(o ports.KV) error {
	if o.Key == "" || looksLikeFlag(o.Key) {
		return domain.ErrArbitraryFlags
	}
	if strings.ContainsAny(o.Key, "=\n\r") || strings.ContainsAny(o.Value, "\n\r") {
		return domain.ErrArbitraryFlags
	}
	if domain.ContainsSecret(o.Value) || domain.ContainsSecret(o.Key) {
		return domain.ErrSecretInArguments
	}
	allowed := map[string]struct{}{
		"rclone.program": {},
		"rclone.args":    {},
		"rclone.timeout": {},
		"vss.timeout":    {},
	}
	if _, ok := allowed[o.Key]; !ok {
		return fmt.Errorf("%w: restic option %s", domain.ErrArbitraryFlags, o.Key)
	}
	if o.Key == "rclone.program" {
		if err := validateRcloneProgram(o.Value); err != nil {
			return err
		}
	}
	if o.Key == "rclone.args" {
		low := strings.ToLower(o.Value)
		if strings.Contains(low, "--rc") || strings.Contains(low, "password") {
			return domain.ErrArbitraryFlags
		}
	}
	return nil
}

// validateRcloneProgram enforces the one shape for -o rclone.program that is
// safe on Windows AND survives restic 0.19.1's parsing:
//
//   - Restic parses the -o value as a CSV field, so a double quote (") or comma
//     is rejected before restic ever looks at it ("bare \" in non-quoted-field").
//   - Restic then runs the value through SplitShellStrings, which treats a bare
//     backslash or whitespace as a separator (so C:\dir\rclone.exe splits into
//     exec: "C:" — DEV-005) and cannot be escaped because quoting is unavailable.
//   - A forward-slash absolute path contains no separator character, so it
//     survives as one literal argument; Go's exec then uses a path containing a
//     slash verbatim, with no PATH or current-directory search. The pinned,
//     digest-verified binary is therefore the only executable that can run.
//
// A bare basename would fall back to PATH/CWD lookup and is rejected. A path with
// a space cannot be expressed safely and is rejected (install rclone space-free).
func validateRcloneProgram(v string) error {
	if strings.ContainsAny(v, "\"'`\\,;|&<>*?\x00\r\n\t ") {
		return fmt.Errorf("%w: rclone.program has an unsafe character", domain.ErrArbitraryFlags)
	}
	if len(v) < 4 || !isASCIILetter(v[0]) || v[1] != ':' || v[2] != '/' {
		return fmt.Errorf("%w: rclone.program must be an absolute forward-slash path", domain.ErrArbitraryFlags)
	}
	if strings.Count(v, ":") != 1 {
		return fmt.Errorf("%w: rclone.program has an unexpected colon", domain.ErrArbitraryFlags)
	}
	for _, seg := range strings.Split(v[3:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: rclone.program path is not normalized", domain.ErrArbitraryFlags)
		}
	}
	base := v[strings.LastIndex(v, "/")+1:]
	if !strings.EqualFold(base, "rclone.exe") && !strings.EqualFold(base, "rclone") {
		return fmt.Errorf("%w: rclone.program must name the rclone executable", domain.ErrArbitraryFlags)
	}
	return nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func rejectExtra(extra []string) error {
	for _, e := range extra {
		if strings.ContainsAny(e, "\n\r") {
			return domain.ErrArbitraryFlags
		}
	}
	return nil
}

func looksLikeFlag(s string) bool {
	return len(s) > 0 && s[0] == '-'
}
