package restic

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Adapter is the only component that understands Restic flags, JSON, and exit codes.
type Adapter struct {
	Runner     ports.Runner
	Secrets    ports.SecretProvider
	Connector  ports.Connector
	Pin        domain.BinaryPin
	TempDir    string
	CacheDir   string
	SystemRoot string
	ExtraPath  []string
	// GracePeriod is the bounded wait after cancel before TerminateJobObject.
	GracePeriod time.Duration
	// ForceTerminate is required in Windows Service mode: no console Ctrl-Break.
	ForceTerminate bool
	// StallTimeout stops a backup whose progress numbers have not moved for
	// this long (0 = DefaultStallTimeout). See Backup.
	StallTimeout time.Duration
}

// DefaultStallTimeout: a backup that makes no progress for this long is
// stopped and reported as a network failure. Confirmed live: when a PC's
// internet connection changed mid-upload, restic kept waiting on the dead
// connection -- the gateway saw no request at all (not even restic's
// 5-minute lock refresh) for 94 minutes -- while a fresh restic process
// started right after finished in two minutes.
const DefaultStallTimeout = 20 * time.Minute

func (a *Adapter) Version(ctx context.Context) (string, error) {
	res, err := a.run(ctx, commandKind("version"), ports.BoundRepository{}, nil, nil, 30*time.Second)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(res.Stdout) + string(res.Stderr))
	return strings.TrimSpace(strings.Split(v, "\n")[0]), nil
}

func (a *Adapter) Init(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) (string, error) {
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return "", err
	}
	res, err := a.run(ctx, cmdInit, bound, env, nil, 10*time.Minute)
	if err != nil {
		return "", err
	}
	cl := classifyExit(res.ExitCode)
	if !cl.OK {
		return "", a.fail(res, cl, "init")
	}
	return a.CatConfig(ctx, repo, password)
}

func (a *Adapter) CatConfig(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) (string, error) {
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return "", err
	}
	res, err := a.run(ctx, cmdCatConfig, bound, env, nil, 10*time.Minute)
	if err != nil {
		return "", err
	}
	cl := classifyExit(res.ExitCode)
	if !cl.OK {
		return "", a.fail(res, cl, "cat config")
	}
	id, err := parseRepoConfig(res.Stdout)
	if err != nil {
		return "", fmt.Errorf("parse repo config: %w", err)
	}
	if repo.ExpectedRepoID != "" && !strings.EqualFold(id, repo.ExpectedRepoID) {
		return id, fmt.Errorf("%w: got %s want %s", domain.ErrRepositoryBinding, id, repo.ExpectedRepoID)
	}
	return id, nil
}

func (a *Adapter) Snapshots(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) ([]ports.Snapshot, error) {
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return nil, err
	}
	res, err := a.run(ctx, cmdSnapshots, bound, env, nil, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	cl := classifyExit(res.ExitCode)
	if !cl.OK {
		return nil, a.fail(res, cl, "snapshots")
	}
	return parseSnapshots(res.Stdout)
}

func (a *Adapter) ReadSnapshot(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, id domain.SnapshotID) (*ports.Snapshot, error) {
	snaps, err := a.Snapshots(ctx, repo, password)
	if err != nil {
		return nil, err
	}
	if snap := matchSnapshot(snaps, id); snap != nil {
		return snap, nil
	}
	return nil, domain.ErrSnapshotReadback
}

// maxLsEntries caps how many entries a single ListSnapshot call returns.
// A folder is browsed one level at a time, so a real directory should never
// come close to this; it exists only so an unexpectedly huge flat directory
// cannot blow up a command's ack payload.
const maxLsEntries = 5000

func (a *Adapter) ListSnapshot(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error) {
	want := strings.ToLower(strings.TrimSpace(snapshotID))
	if len(want) < 8 || len(want) > 64 || !isHexID(want) {
		return nil, false, fmt.Errorf("%w: snapshot id", domain.ErrArbitraryFlags)
	}
	if strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\n\r") {
		return nil, false, domain.ErrRestorePathRejected
	}
	extra := []string{want}
	if prefix != "" {
		extra = append(extra, prefix)
	}
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return nil, false, err
	}
	res, err := a.run(ctx, cmdLS, bound, env, extra, 10*time.Minute)
	if err != nil {
		return nil, false, err
	}
	cl := classifyExit(res.ExitCode)
	if !cl.OK {
		return nil, false, a.fail(res, cl, "ls")
	}
	entries := parseLsEntries(res.Stdout, prefix)
	truncated := len(entries) > maxLsEntries
	if truncated {
		entries = entries[:maxLsEntries]
	}
	return entries, truncated, nil
}

// ListSnapshotFull enumerates an entire snapshot in one restic process --
// every file and directory at any depth, for the permanent catalog. Unlike
// ListSnapshot (one browse level, prefix-scoped), this call takes no
// prefix: restic's own `ls <snapshot>` with no path argument already
// walks the whole tree (see parseLsEntries's doc comment above).
func (a *Adapter) ListSnapshotFull(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, snapshotID string) ([]ports.SnapshotEntry, error) {
	want := strings.ToLower(strings.TrimSpace(snapshotID))
	if len(want) < 8 || len(want) > 64 || !isHexID(want) {
		return nil, fmt.Errorf("%w: snapshot id", domain.ErrArbitraryFlags)
	}
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return nil, err
	}
	res, err := a.run(ctx, cmdLS, bound, env, []string{want}, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	cl := classifyExit(res.ExitCode)
	if !cl.OK {
		return nil, a.fail(res, cl, "ls")
	}
	return parseLsEntriesFull(res.Stdout), nil
}

func matchSnapshot(snaps []ports.Snapshot, id domain.SnapshotID) *ports.Snapshot {
	want := strings.ToLower(strings.TrimSpace(string(id)))
	if len(want) < 8 || len(want) > 64 || !isHexID(want) {
		return nil
	}
	var hit *ports.Snapshot
	for i := range snaps {
		got := strings.ToLower(string(snaps[i].ID))
		if got == want || strings.HasPrefix(got, want) {
			if hit != nil {
				return nil
			}
			hit = &snaps[i]
		}
	}
	if hit == nil || !domain.SnapshotIDLooksPlausible(string(hit.ID)) {
		return nil
	}
	return hit
}

func isHexID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (a *Adapter) Check(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef, readData bool) (ports.CheckResult, error) {
	bound, env, err := a.bind(ctx, repo, password)
	if err != nil {
		return ports.CheckResult{}, err
	}
	var extra []string
	if readData {
		extra = append(extra, "--read-data")
	}
	res, err := a.run(ctx, cmdCheck, bound, env, extra, 2*time.Hour)
	if err != nil {
		return ports.CheckResult{}, err
	}
	cl := classifyExit(res.ExitCode)
	out := sanitizeDiagnostic(string(res.Stdout) + "\n" + string(res.Stderr))
	return ports.CheckResult{
		OK:       cl.OK && !res.Cancelled && !res.TimedOut && ctx.Err() == nil && !res.StdoutTruncated && !res.StderrTruncated,
		ReadData: readData,
		Output:   out,
		ExitCode: res.ExitCode,
	}, nil
}

func (a *Adapter) stallTimeout() time.Duration {
	if a.StallTimeout > 0 {
		return a.StallTimeout
	}
	return DefaultStallTimeout
}

// watchStall returns a context that is cancelled once the parser has seen no
// change in restic's progress numbers for stallTimeout, and whether that
// happened. Call stop when the run is over.
func (a *Adapter) watchStall(ctx context.Context, p *backupProgressParser) (context.Context, func() bool, func()) {
	limit := a.stallTimeout()
	runCtx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	last := time.Now()
	fired := false
	p.onActivity = func() {
		mu.Lock()
		last = time.Now()
		mu.Unlock()
	}
	tick := limit / 20
	if tick > 30*time.Second {
		tick = 30 * time.Second
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-runCtx.Done():
				return
			case <-t.C:
				mu.Lock()
				stuck := time.Since(last) > limit
				if stuck {
					fired = true
				}
				mu.Unlock()
				if stuck {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	stop := func() { once.Do(func() { close(done); cancel() }) }
	stalled := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fired
	}
	return runCtx, stalled, stop
}

func resOutput(res *ports.ProcessResult) ([]byte, []byte) {
	if res == nil {
		return nil, nil
	}
	return res.Stdout, res.Stderr
}

func (a *Adapter) Backup(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
	started := time.Now()
	out := domain.BackupResult{Outcome: domain.PhaseFailed, StartedAt: started}
	if err := req.Validate(); err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	bound, env, err := a.bind(ctx, req.Repository, req.PasswordRef)
	if err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	extra, err := backupExtras(req)
	if err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	timeout := time.Until(req.Deadline)
	if timeout <= 0 {
		timeout = 8 * time.Hour
	}
	progress := newBackupProgressParser(req.Progress)
	// restic prints no status at all when stdout is not a terminal (always,
	// under the Windows service) unless RESTIC_PROGRESS_FPS is set; the
	// VSS-required path runs without --json, so without this the panel and
	// Stowline Backups showed "0%" for the whole run. One line every 2 s.
	env = append(env, ports.EnvVar{Name: "RESTIC_PROGRESS_FPS", Value: "0.5"})
	runCtx, stalled, stopWatch := a.watchStall(ctx, progress)
	res, err := a.runObserved(runCtx, cmdBackup, bound, env, extra, timeout, progress.Feed, progress.Feed)
	stopWatch()
	out.EndedAt = time.Now()
	out.Duration = out.EndedAt.Sub(started)
	if stalled() && (res == nil || res.Cancelled) {
		if sum, _ := parseBackupSummary(engineOutput(resOutput(res))); sum.SnapshotID == "" {
			out.ErrorClass = domain.ErrorNetwork
			out.ErrorMessage = fmt.Sprintf("no backup progress for %s (internet connection likely lost); stopped so the next attempt starts fresh", a.stallTimeout())
			if res != nil {
				out.ExitCode, out.ExitKnown = res.ExitCode, true
			}
			return out, nil
		}
	}
	out.Duration = out.EndedAt.Sub(started)
	if err != nil && res == nil {
		out.ErrorClass = domain.ErrorInternal
		out.ErrorMessage = err.Error()
		return out, err
	}
	if res != nil && (res.Cancelled || res.TimedOut) {
		sum, _ := parseBackupSummary(engineOutput(res.Stdout, res.Stderr))
		out.Summary = sum
		out.ExitCode = res.ExitCode
		out.ExitKnown = true
		out.ErrorClass = domain.ErrorCancelled
		if sum.SnapshotID != "" {
			out.SnapshotID = domain.SnapshotID(sum.SnapshotID)
			durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			snap, rerr := a.ReadSnapshot(durable, req.Repository, req.PasswordRef, out.SnapshotID)
			out.ReadbackOK = rerr == nil && snap != nil
			if snap != nil {
				out.SnapshotID = snap.ID
				out.Summary.SnapshotID = string(snap.ID)
			}
			out.Outcome = domain.PhasePartial
			out.PublishedNotGreen = true
			out.ErrorMessage = "process interrupted after snapshot publish"
			return out, nil
		}
		out.Outcome = domain.PhaseCancelled
		if res.TimedOut {
			out.ErrorMessage = "process deadline exceeded"
		} else {
			out.ErrorMessage = "process cancelled"
		}
		return out, nil
	}
	cl := classifyExit(res.ExitCode)
	out.ExitCode = res.ExitCode
	out.ExitKnown = cl.Known
	combined := engineOutput(res.Stdout, res.Stderr)
	low := strings.ToLower(string(combined))
	if strings.Contains(low, "insufficient backup privileges") || strings.Contains(low, "e_accessdenied") {
		cl.Class = domain.ErrorVSS
		out.ErrorClass = domain.ErrorVSS
	}
	sum, _ := parseBackupSummary(combined)
	out.Summary = sum
	if sum.SnapshotID != "" {
		out.SnapshotID = domain.SnapshotID(sum.SnapshotID)
	}
	ver, _ := a.cachedVersion(ctx)
	vols := volumesForRoots(req.SourceRoots)
	out.VSS = parseVSSEvidenceFromProcess(res.Stdout, res.Stderr, res.StdoutTruncated || res.StderrTruncated, req.VSSMode, vols, ver)
	out.Consistency = out.VSS.Class()

	if !cl.Known {
		out.Outcome = domain.PhaseFailed
		out.ErrorClass = domain.ErrorInternal
		out.ErrorMessage = domain.ErrUnknownExitCode.Error()
		return out, domain.ErrUnknownExitCode
	}
	if cl.Fatal && !cl.Partial {
		out.Outcome = domain.PhaseFailed
		out.ErrorClass = cl.Class
		out.ErrorMessage = a.diag(res)
		if out.SnapshotID != "" {
			out.PublishedNotGreen = true
		}
		return out, nil
	}
	if out.SnapshotID == "" {
		out.Outcome = domain.PhaseFailed
		out.ErrorClass = domain.ErrorIntegrity
		out.ErrorMessage = domain.ErrMissingSnapshotID.Error()
		return out, nil
	}

	snap, err := a.ReadSnapshot(ctx, req.Repository, req.PasswordRef, out.SnapshotID)
	out.ReadbackOK = err == nil && snap != nil
	if !out.ReadbackOK {
		out.Outcome = domain.PhaseFailed
		out.ErrorClass = domain.ErrorIntegrity
		out.ErrorMessage = domain.ErrSnapshotReadback.Error()
		out.PublishedNotGreen = true
		return out, nil
	}
	out.SnapshotID = snap.ID
	out.Summary.SnapshotID = string(snap.ID)
	if req.Repository.ExpectedRepoID != "" {
		id, err := a.CatConfig(ctx, req.Repository, req.PasswordRef)
		if err != nil || !strings.EqualFold(id, req.Repository.ExpectedRepoID) {
			out.Outcome = domain.PhaseFailed
			out.ErrorClass = domain.ErrorIntegrity
			out.ErrorMessage = domain.ErrRepositoryBinding.Error()
			out.RepositoryID = id
			return out, nil
		}
		out.RepositoryID = id
	}
	if req.VSSMode == domain.VSSRequired && out.Consistency != domain.ConsistencyVerified {
		out.Outcome = domain.PhasePartial
		out.ErrorClass = domain.ErrorConsistencyNotMet
		out.ErrorMessage = domain.ErrConsistencyNotMet.Error()
		out.PublishedNotGreen = true
		return out, nil
	}
	if cl.Partial {
		out.Outcome = domain.PhasePartial
		out.ErrorClass = domain.ErrorSourceUnreadable
		out.ErrorMessage = domain.ErrPartialNotSuccess.Error()
		out.PublishedNotGreen = true
		return out, nil
	}
	out.Outcome = domain.PhaseSucceeded
	out.RequiredRootsOK = true
	out.CleanupOK = true
	return out, nil
}

func (a *Adapter) Restore(ctx context.Context, req domain.RestoreRequest) (domain.RestoreResult, error) {
	started := time.Now()
	out := domain.RestoreResult{State: domain.RestoreFailed}
	if err := req.Validate(); err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	bound, env, err := a.bind(ctx, req.Repository, req.PasswordRef)
	if err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	extra, err := restoreExtras(req)
	if err != nil {
		out.ErrorClass = domain.ErrorConfig
		out.ErrorMessage = err.Error()
		return out, err
	}
	timeout := time.Until(req.Deadline)
	if timeout <= 0 {
		timeout = 8 * time.Hour
	}
	var onOut func([]byte)
	if req.Progress != nil {
		onOut = newRestoreProgressParser(req.Progress).Feed
	}
	res, err := a.runObserved(ctx, cmdRestore, bound, env, extra, timeout, onOut, nil)
	out.Duration = time.Since(started)
	if err != nil && res == nil {
		out.ErrorMessage = err.Error()
		out.ErrorClass = domain.ErrorInternal
		return out, err
	}
	if res.Cancelled || res.TimedOut || ctx.Err() != nil {
		out.State = domain.RestoreCancelled
		out.ErrorClass = domain.ErrorCancelled
		return out, nil
	}
	cl := classifyExit(res.ExitCode)
	out.ExitCode = res.ExitCode
	if !cl.OK {
		out.ErrorClass = cl.Class
		if !cl.Known {
			out.ErrorClass = domain.ErrorInternal
			out.ErrorMessage = domain.ErrUnknownExitCode.Error()
			return out, domain.ErrUnknownExitCode
		}
		out.ErrorMessage = a.diag(res)
		return out, nil
	}
	out.VerifiedByEngine = req.VerifyContent
	out.State = domain.RestoreVerifying
	out.Destination = req.DestinationDir
	return out, nil
}

func (a *Adapter) bind(ctx context.Context, repo domain.RepositoryDescriptor, password domain.SecretRef) (ports.BoundRepository, []ports.EnvVar, error) {
	if err := repo.Validate(); err != nil {
		return ports.BoundRepository{}, nil, err
	}
	bound, err := a.Connector.Bind(ctx, repo)
	if err != nil {
		return ports.BoundRepository{}, nil, err
	}
	h, err := a.Secrets.Open(ctx, password)
	if err != nil {
		return ports.BoundRepository{}, nil, err
	}
	defer h.Close()
	pw := append([]byte(nil), h.Bytes()...)
	env := []ports.EnvVar{
		{Name: "RESTIC_PASSWORD", Value: string(pw), Secret: true},
	}
	env = append(env, bound.Env...)
	// Credential refs are opened through the provider EACH ref itself
	// declares (openSecretRef), never through a.Secrets: a.Secrets is
	// selected once, by the caller, to match `password`'s provider, and
	// reusing it here silently mis-opened refs of a different declared
	// provider -- e.g. SecretRESTUsername is always Provider=file (plain
	// text), so opening it through a machine-scope DPAPI provider failed
	// "missing DPAPI envelope (refusing raw/plaintext)" on a real PC.
	// Each branch is also gated on backend relevance: a REST_GATEWAY-only
	// credential must never be opened for a LOCAL/rclone repository just
	// because a stale entry happens to exist in CredentialRefs.
	if repo.BackendKind == domain.BackendRESTGateway {
		if ref, ok := repo.CredentialRefs[domain.SecretRESTUsername]; ok {
			u, err := openSecretRef(ctx, ref)
			if err != nil {
				return ports.BoundRepository{}, nil, err
			}
			env = append(env, ports.EnvVar{Name: "RESTIC_REST_USERNAME", Value: string(u.Bytes()), Secret: false})
			_ = u.Close()
		}
		if ref, ok := repo.CredentialRefs[domain.SecretRESTPassword]; ok {
			p, err := openSecretRef(ctx, ref)
			if err != nil {
				return ports.BoundRepository{}, nil, err
			}
			env = append(env, ports.EnvVar{Name: "RESTIC_REST_PASSWORD", Value: string(p.Bytes()), Secret: true})
			_ = p.Close()
		}
	}
	if repo.BackendKind == domain.BackendPilotRcloneDrive || repo.BackendKind == domain.BackendPilotRcloneLocal {
		if ref, ok := repo.CredentialRefs[domain.SecretRcloneConfigPass]; ok {
			unlock, err := openSecretRef(ctx, ref)
			if err != nil {
				return ports.BoundRepository{}, nil, err
			}
			env = append(env, ports.EnvVar{Name: "RCLONE_CONFIG_PASS", Value: string(unlock.Bytes()), Secret: true})
			_ = unlock.Close()
		}
	}
	for i := range pw {
		pw[i] = 0
	}
	return bound, env, nil
}

// openSecretRef opens ref using the provider it itself declares
// (secrets.ForRef), never a provider pre-selected for some other ref. A
// repository can legitimately mix providers -- e.g. REST_GATEWAY has a
// DPAPI-machine repository password alongside a plain-file REST username --
// and each provider already fails closed on the other's shape
// (FileProvider refuses ".dpapi" locators and DPAPI-magic content;
// DPAPIProvider refuses anything that doesn't decode as its own envelope),
// so there is no silent raw/plaintext fallback in either direction.
func openSecretRef(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	prov, _, err := secrets.ForRef(ref)
	if err != nil {
		return nil, err
	}
	return prov.Open(ctx, ref)
}

func (a *Adapter) run(ctx context.Context, kind commandKind, bound ports.BoundRepository, env []ports.EnvVar, extra []string, timeout time.Duration) (*ports.ProcessResult, error) {
	return a.runObserved(ctx, kind, bound, env, extra, timeout, nil, nil)
}

func (a *Adapter) runObserved(ctx context.Context, kind commandKind, bound ports.BoundRepository, env []ports.EnvVar, extra []string, timeout time.Duration, onStdout, onStderr func([]byte)) (*ports.ProcessResult, error) {
	args, err := buildArgs(kind, bound, extra...)
	if err != nil {
		return nil, err
	}
	restricted, err := buildChildEnv(env, a.CacheDir, a.TempDir, a.SystemRoot, a.pathDirs())
	if err != nil {
		return nil, err
	}
	spec := ports.Spec{
		Name:            "restic-" + string(kind),
		Executable:      a.Pin.Path,
		Args:            args,
		Env:             restricted,
		Dir:             a.TempDir,
		Timeout:         timeout,
		GracePeriod:     a.killAfter(),
		ForceTerminate:  a.ForceTerminate,
		ExpectedSHA256:  a.Pin.SHA256,
		ExpectedVersion: a.Pin.Version,
		Dependencies:    bound.BinaryPins,
		OnStdout:        onStdout,
		OnStderr:        onStderr,
	}
	return a.Runner.Run(ctx, spec)
}

func (a *Adapter) killAfter() time.Duration {
	if a.GracePeriod > 0 {
		return a.GracePeriod
	}
	if a.ForceTerminate {
		return 2 * time.Second
	}
	return 2 * time.Second
}

func (a *Adapter) fail(res *ports.ProcessResult, cl classifiedExit, op string) error {
	msg := a.diag(res)
	if cl.Class == domain.ErrorAuth {
		return fmt.Errorf("%w: %s (exit %d): %s", domain.ErrAuth, op, res.ExitCode, msg)
	}
	if !cl.Known {
		return fmt.Errorf("%w: %s: %s", domain.ErrUnknownExitCode, op, msg)
	}
	return fmt.Errorf("%s failed (exit %d): %s", op, res.ExitCode, msg)
}

func (a *Adapter) diag(res *ports.ProcessResult) string {
	if res == nil {
		return ""
	}
	if ee := parseExitError(res.Stderr); ee != nil {
		return sanitizeDiagnostic(ee.Message)
	}
	return sanitizeDiagnostic(string(res.Stderr))
}

func (a *Adapter) cachedVersion(ctx context.Context) (string, error) {
	return a.Version(ctx)
}

func (a *Adapter) pathDirs() []string {
	var dirs []string
	if a.Pin.Path != "" {
		dirs = append(dirs, filepath.Dir(a.Pin.Path))
	}
	dirs = append(dirs, a.ExtraPath...)
	return dirs
}

func buildChildEnv(extra []ports.EnvVar, cache, temp, sysroot string, extraPath []string) ([]ports.EnvVar, error) {
	return process.RestrictedEnv(extra, cache, temp, sysroot, extraPath)
}
