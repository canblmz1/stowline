package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/application"
	"github.com/canblmz1/stowline/agent/internal/buildinfo"
	"github.com/canblmz1/stowline/agent/internal/config"
	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
	"github.com/canblmz1/stowline/agent/internal/pilot/inventory"
	"github.com/canblmz1/stowline/agent/internal/pilot/preflight"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/qualification"
	"github.com/canblmz1/stowline/agent/internal/scheduler"
	"github.com/canblmz1/stowline/agent/internal/telemetry"
	"github.com/canblmz1/stowline/agent/internal/windows/identity"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help" {
		usage()
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if err := dispatch(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", domain.Redact(err.Error()))
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `stowline-agent — Stowline Windows agent (pilot)

Commands:
  version
  help | --help
  inventory
  corpus generate
  config write
  prove p0b|p0c|rclone-local|cancel
  backup
  restore --snapshot <64-hex> [--job <id>]
  snapshots
  check [--read-data]
  canary
  status
  control enroll --url <control-plane> --token <enrollment-token> [--mode=pilot|service]
  service preflight [--json <path>]
  secrets provision --mode=pilot|service
  service install|remove|start|stop|run

Pilot only. Does not run restic forget/prune/unlock/key.
Exit 2 = usage; exit 1 = command failure (including unsuccessful preflight); exit 0 = success.
`)
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "version":
		fmt.Println("stowline-agent " + buildinfo.Version)
		return nil
	case "inventory":
		return cmdInventory()
	case "corpus":
		if len(args) < 1 || args[0] != "generate" {
			return fmt.Errorf("usage: stowline-agent corpus generate")
		}
		cfg := config.DefaultPilot()
		_, err := corpus.Generate(cfg.SourceRoots[0])
		return err
	case "config":
		if len(args) < 1 || args[0] != "write" {
			return fmt.Errorf("usage: stowline-agent config write")
		}
		return writeConfig()
	case "control":
		return cmdControl(args)
	case "secrets":
		return cmdSecrets(args)
	case "service":
		if len(args) < 1 {
			return fmt.Errorf("usage: stowline-agent service install|remove|start|stop|run|preflight")
		}
		if args[0] == "preflight" {
			return cmdPreflight(args[1:])
		}
		return cmdService(args[0])
	case "prove":
		if len(args) < 1 {
			return fmt.Errorf("usage: stowline-agent prove p0b|p0c|rclone-local|cancel")
		}
		switch args[0] {
		case "p0b":
			return proveP0B()
		case "p0c":
			return proveP0C()
		case "rclone-local":
			return proveRcloneLocal()
		case "cancel":
			return proveCancel()
		default:
			return fmt.Errorf("unknown prove target")
		}
	case "backup":
		return cmdBackup()
	case "restore":
		return cmdRestore(args)
	case "snapshots":
		return cmdSnapshots()
	case "check":
		readData := false
		for _, a := range args {
			if a == "--read-data" {
				readData = true
			}
		}
		return cmdCheck(readData)
	case "canary":
		return cmdCanary(args)
	case "status":
		return cmdStatus()
	default:
		usage()
		return fmt.Errorf("unknown command %s", cmd)
	}
}

func writeConfig() error {
	cfg := config.DefaultPilot()
	if err := os.MkdirAll(filepath.Join(cfg.PilotRoot, "config"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StagingRoot, 0700); err != nil {
		return err
	}
	if err := provisionSecrets(&cfg, "pilot"); err != nil {
		return err
	}
	return persistConfig(cfg)
}

func cmdInventory() error {
	cfg := config.DefaultPilot()
	_ = os.MkdirAll(cfg.TempDir, 0700)
	_ = os.MkdirAll(cfg.CacheDir, 0700)
	ctx := context.Background()
	eng := &restic.Adapter{
		Runner:    process.NewRunner(),
		Secrets:   secrets.StaticProvider{},
		Connector: storage.Connector{RclonePin: cfg.Binaries.Rclone},
		Pin:       cfg.Binaries.Restic,
		TempDir:   cfg.TempDir,
		CacheDir:  cfg.CacheDir,
		ExtraPath: []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
	}
	resticVer, _ := eng.Version(ctx)
	rcloneVer := rcloneVersion(ctx, &cfg)
	rep, err := inventory.Collect(resticVer, rcloneVer, cfg.SourceRoots[0])
	if err != nil {
		return err
	}
	path, err := runtimeEvidencePath("environment.md")
	if err != nil {
		return err
	}
	if err := inventory.WriteMarkdown(path, rep); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func runtimeEvidenceDir() (string, error) {
	dir := `C:\Stowline\cache\evidence`
	if err := os.MkdirAll(dir, 0700); err != nil {
		dir = filepath.Join(os.TempDir(), "stowline-evidence")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func runtimeEvidencePath(name string) (string, error) {
	dir, err := runtimeEvidenceDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func writeRuntimeEvidence(name string, payload []byte) (string, error) {
	out, err := runtimeEvidencePath(name)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, payload, 0644); err != nil {
		return "", err
	}
	return out, nil
}

func rcloneVersion(ctx context.Context, cfg *config.File) string {
	res, err := process.NewRunner().Run(ctx, ports.Spec{
		Executable:     cfg.Binaries.Rclone.Path,
		Args:           []string{"version"},
		ExpectedSHA256: cfg.Binaries.Rclone.SHA256,
		Timeout:        30 * time.Second,
	})
	if err != nil || res == nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

func cmdBackup() error {
	cfg, _, svc, err := load()
	if err != nil {
		return err
	}
	up, down, err := domain.CLIWANLimits(cfg.Repository.BackendKind, cfg.Repository.Capabilities.QualificationOnly)
	if err != nil {
		return err
	}
	req := domain.BackupRequest{
		DeviceID:       cfg.DeviceID,
		InstallationID: cfg.InstallationID,
		Repository:     cfg.Repository,
		SourceRoots:    cfg.SourceRoots,
		VSSMode:        cfg.VSSMode,
		PasswordRef:    cfg.PasswordRef,
		CacheDir:       cfg.CacheDir,
		Deadline:       time.Now().Add(cfgDeadline()),
		Host:           cfg.DeviceID,
		BandwidthKiBps: up,
	}
	_ = down
	res, err := svc.Backup(context.Background(), req)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func cmdRestore(args []string) error {
	var snap, job string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--snapshot":
			i++
			if i < len(args) {
				snap = args[i]
			}
		case "--job":
			i++
			if i < len(args) {
				job = args[i]
			}
		}
	}
	if snap == "" {
		return fmt.Errorf("--snapshot required")
	}
	cfg, _, svc, err := load()
	if err != nil {
		return err
	}
	_, down, err := domain.CLIWANLimits(cfg.Repository.BackendKind, cfg.Repository.Capabilities.QualificationOnly)
	if err != nil {
		return err
	}
	req := domain.RestoreRequest{
		SnapshotID:           domain.SnapshotID(snap),
		StagingRoot:          cfg.StagingRoot,
		PasswordRef:          cfg.PasswordRef,
		Repository:           cfg.Repository,
		VerifyContent:        true,
		Deadline:             time.Now().Add(cfgDeadline()),
		RestoreDownloadKiBps: down,
		BandwidthKiBps:       down,
	}
	if job != "" {
		req.JobID = domain.JobID(job)
	}
	res, err := svc.Restore(context.Background(), req)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func cmdSnapshots() error {
	cfg, eng, _, err := load()
	if err != nil {
		return err
	}
	snaps, err := eng.Snapshots(context.Background(), cfg.Repository, cfg.PasswordRef)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(snaps)
}

func cmdCheck(readData bool) error {
	cfg, eng, _, err := load()
	if err != nil {
		return err
	}
	res, err := eng.Check(context.Background(), cfg.Repository, cfg.PasswordRef, readData)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func cmdService(op string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch op {
	case "install":
		return winsvc.Install(exe)
	case "remove":
		return winsvc.Remove()
	case "start":
		cfg, err := config.Load(`C:\Stowline\config\pilot.json`)
		if err != nil {
			return err
		}
		if err := preflight.RefuseServiceStart(preflight.Run(cfg)); err != nil {
			return err
		}
		return winsvc.Control("start")
	case "stop":
		return winsvc.Control("stop")
	case "run":
		if exePath, err := os.Executable(); err == nil {
			// A freshly auto-upgraded binary that never reaches a healthy
			// heartbeat is put back to the previous one after a few boots
			// instead of crash-looping under SCM's restart policy forever.
			if rolled, gerr := guardUnconfirmedUpgrade(exePath); gerr != nil {
				fmt.Fprintf(os.Stderr, "upgrade guard: %s\n", domain.Redact(gerr.Error()))
			} else if rolled {
				fmt.Fprintln(os.Stderr, "new agent build never confirmed healthy; previous build restored, restarting")
				os.Exit(1)
			}
			pruneUpgradeLeftovers(exePath, 2)
		}
		cfg, eng, svcApp, err := loadMode(winsvc.IsServiceContext())
		if err != nil {
			return err
		}
		pol := domain.DefaultPilotPolicy(cfg.SourceRoots)
		if latest, err := svcApp.Journal.LatestPolicy(context.Background()); err == nil && latest != nil {
			if latest.ValidateForBackend(cfg.Repository.BackendKind) == nil {
				pol = *latest
			} else if !domain.RequiresWANRateLimit(cfg.Repository.BackendKind) && latest.Validate() == nil {
				pol = *latest
			}
		}
		live := &livePolicy{}
		live.set(pol)
		// sharedProgress outlives any single backup -- the local status
		// endpoint (see localui.go) reads it between runs too, so unlike the
		// old per-run `progress` variable it must be one instance reused for
		// the whole service lifetime, explicitly reset at the start of each
		// run and cleared when one ends.
		sharedProgress := &liveBackupProgress{}
		// busy counts backups and restores in flight. An automatic upgrade
		// restarts the service, so it never starts while this is non-zero
		// and, once staged, waits for it to drop to zero before restarting.
		var busy atomic.Int32
		var upgradeRestartPending atomic.Bool
		ctrl := &scheduler.Controller{
			DeviceID:         cfg.DeviceID,
			InstallationID:   cfg.InstallationID,
			Policy:           pol.Schedule,
			PolicyRevisionID: pol.RevisionID,
			Backoff:          scheduler.DefaultBackoff,
			Clock:            ports.RealClock{},
			Journal:          svcApp.Journal,
			StartedAt:        time.Now(),
			Reconcile: func(ctx context.Context) error {
				_, err := svcApp.Reconcile(ctx, cfg.Repository, cfg.PasswordRef)
				return err
			},
		}
		var exec *ctrlclient.Executor
		ctrl.Backup = func(ctx context.Context, slot string) error {
			busy.Add(1)
			defer busy.Add(-1)
			cur := live.get()
			last, _ := svcApp.Journal.LatestSucceededBackupSlot(ctx)
			class := wanClass(last)
			// Previously compared a 12-byte slice against the 8-byte
			// literal "control|" (never equal), so every operator-triggered
			// RUN_BACKUP silently fell through to wanClass(last)'s
			// WANClassInitialSeed for any device with no prior successful
			// backup -- competing for the server's single site-wide seed
			// admission slot instead of being the normal-priority operator
			// click it is. Confirmed live: a synthetic device's first
			// "Backup Now" click queued behind another device's seed lease
			// and never ran. domain.IsControlSlotKey uses strings.HasPrefix,
			// which cannot repeat this class of bug.
			operatorTriggered := domain.IsControlSlotKey(slot)
			if operatorTriggered {
				class = domain.WANClassNormalBackup
			} else if domain.IsQualificationSlotKey(slot) {
				class = domain.WANClassCanary
			}
			// An operator's explicit "Backup Now" click must not silently do
			// nothing: only the scheduled/automatic path (any other slot
			// format) treats a WAN admission decline as a normal "wait my
			// turn," not an error.
			bandwidth := cur.BandwidthKiBps
			download := cur.RestoreDownloadKiBps
			var progress *liveBackupProgress
			defer sharedProgress.clear()
			if domain.RequiresWANRateLimit(cfg.Repository.BackendKind) {
				if reason := refuseWANStart(exec, cur, cfg.Repository.BackendKind); reason != "" {
					if operatorTriggered {
						return &domain.WANAdmissionDeclinedError{Reason: "LOCAL_PRECHECK_" + reason}
					}
					return nil
				}
				body := map[string]any{
					"job_id":          string(domain.NewJobID()),
					"attempt_id":      string(domain.NewAttemptID()),
					"slot_key":        slot,
					"class":           string(class),
					"installation_id": cfg.InstallationID,
				}
				lease, err := exec.Client.AcquireLease(ctx, body)
				if err != nil || lease == nil || !lease.Granted() {
					if operatorTriggered {
						reason := leaseStatus(lease)
						if err != nil {
							reason = "LEASE_HTTP_ERROR"
						}
						return &domain.WANAdmissionDeclinedError{Reason: reason}
					}
					return nil
				}
				bandwidth = lease.BandwidthKiBps
				if lease.RestoreDownloadKiBps > 0 {
					download = lease.RestoreDownloadKiBps
				}
				// Report an explicit 0% on the first lease renewal so the panel can
				// distinguish this progress-capable build from older agents while
				// restic is still performing its initial scan.
				sharedProgress.resetForNewRun()
				progress = sharedProgress
				stop := holdLease(ctx, exec.Client, lease, progress)
				defer stop()
			} else if err := cur.ValidateForBackend(cfg.Repository.BackendKind); err != nil {
				return err
			}
			if progress == nil {
				// No WAN lease to report through, but the local desktop page
				// still reads sharedProgress -- a local/LAN repository must show
				// the user a percentage too.
				sharedProgress.resetForNewRun()
				progress = sharedProgress
			}
			dead := deadlineFor(time.Now(), cur.Schedule, class)
			ctx, cancel := context.WithDeadline(ctx, dead)
			defer cancel()
			// Every operator "Backup Now" click is its own logical unit of
			// work. A fixed per-device SlotKey here (unlike the scheduler's
			// once-per-day date slot, or a qualification run's once-per-run
			// slot) would permanently collapse every future click into the
			// first one's cached JobSucceeded result -- restic would never
			// run again and later file changes would never be seen.
			// Redelivery of the SAME click is already made safe at the
			// command layer by ConsumeCommand/CommandResult, so this
			// job-level slot only needs to exist for the scheduler and
			// qualification callers.
			backupSlot := slot
			if operatorTriggered {
				backupSlot = ""
			}
			var reportProgress func(domain.BackupProgress)
			if progress != nil {
				reportProgress = progress.update
			}
			res, err := svcApp.Backup(ctx, domain.BackupRequest{
				DeviceID:         cfg.DeviceID,
				InstallationID:   cfg.InstallationID,
				PolicyRevisionID: cur.RevisionID,
				Repository:       cfg.Repository,
				SourceRoots:      currentSourceRoots(filepath.Join(cfg.PilotRoot, "config", "pilot.json"), cfg.SourceRoots),
				Excludes:         currentExcludes(filepath.Join(cfg.PilotRoot, "config", "pilot.json")),
				VSSMode:          cfg.VSSMode,
				PasswordRef:      cfg.PasswordRef,
				CacheDir:         cfg.CacheDir,
				Deadline:         dead,
				Host:             cfg.DeviceID,
				SlotKey:          backupSlot,
				FreeSpaceFloor:   cur.FreeSpaceFloorBytes,
				BandwidthKiBps:   bandwidth,
				WANClass:         class,
				Progress:         reportProgress,
			})
			_ = download
			if operatorTriggered {
				return winsvc.WrapOperatorBackup(res, err)
			}
			return winsvc.WrapBackup(res, err)
		}
		// restoreWithAdmission is the single restore path for both the
		// panel's RESTORE_TO_STAGING command and the local desktop page, so
		// a self-service restore gets the same WAN admission lease and
		// bandwidth limit as an admin-initiated one instead of bypassing
		// them. Returns where the files landed (always the staging root).
		restoreWithAdmission := func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			busy.Add(1)
			defer busy.Add(-1)
			cur := live.get()
			download := cur.RestoreDownloadKiBps
			if download == 0 {
				download = cur.BandwidthKiBps
			}
			if domain.RequiresWANRateLimit(cfg.Repository.BackendKind) {
				if exec == nil || exec.Client == nil {
					return "", fmt.Errorf("%w: restore requires control plane admission", domain.ErrConfig)
				}
				lease, err := exec.Client.AcquireLease(ctx, map[string]any{
					"job_id": jobID, "attempt_id": string(domain.NewAttemptID()),
					"class": string(domain.WANClassRestore), "installation_id": cfg.InstallationID,
				})
				if err != nil {
					return "", err
				}
				if lease == nil || !lease.Granted() {
					return "", fmt.Errorf("%w: restore admission %s", domain.ErrConfig, leaseStatus(lease))
				}
				if lease.RestoreDownloadKiBps > 0 {
					download = lease.RestoreDownloadKiBps
				}
				stop := holdLease(ctx, exec.Client, lease, nil)
				defer stop()
			}
			res, err := svcApp.Restore(ctx, domain.RestoreRequest{
				JobID:                domain.JobID(jobID),
				SnapshotID:           domain.SnapshotID(snapshot),
				Selections:           selections,
				StagingRoot:          cfg.StagingRoot,
				PasswordRef:          cfg.PasswordRef,
				Repository:           cfg.Repository,
				VerifyContent:        true,
				Deadline:             time.Now().Add(2 * time.Hour),
				RestoreDownloadKiBps: download,
				BandwidthKiBps:       download,
				Progress:             progress,
			})
			if err != nil {
				return "", err
			}
			if res.State != domain.RestoreReady {
				return "", fmt.Errorf("restore state %s", res.State)
			}
			return res.Destination, nil
		}
		if cfg.ControlPlaneURL != "" {
			cred := ""
			if cfg.ControlSecretRef.Locator != "" {
				if prov, _, err := secrets.ForRef(cfg.ControlSecretRef); err == nil {
					if h, err := prov.Open(context.Background(), cfg.ControlSecretRef); err == nil {
						cred = string(append([]byte(nil), h.Bytes()...))
						_ = h.Close()
					}
				}
			}
			ctrlClient := &ctrlclient.Client{BaseURL: cfg.ControlPlaneURL, Credential: cred}
			exec = &ctrlclient.Executor{
				Client:      ctrlClient,
				Journal:     svcApp.Journal,
				Version:     buildinfo.Version,
				AgentSHA256: selfSHA256(),
				PolicyRev:   pol.RevisionID,
				Cancel:      svcApp.CancelCurrent,
				OnPolicy: func(p domain.LocalPolicy) {
					live.set(p)
					ctrl.SetPolicy(p.Schedule, p.RevisionID)
				},
				Backup: func(ctx context.Context) error {
					return ctrl.Backup(ctx, domain.ControlSlotKey(cfg.DeviceID))
				},
				Restore: func(ctx context.Context, jobID, snapshot string, selections []string) error {
					_, err := restoreWithAdmission(ctx, jobID, snapshot, selections, nil)
					return err
				},
				Canary: func(ctx context.Context) error {
					return cmdCanary(nil)
				},
				Browse: func(ctx context.Context, snapshot, prefix string) (map[string]any, error) {
					entries, truncated, err := eng.ListSnapshot(ctx, cfg.Repository, cfg.PasswordRef, snapshot, prefix)
					if err != nil {
						return nil, err
					}
					return map[string]any{"entries": entries, "prefix": prefix, "truncated": truncated, "count": len(entries)}, nil
				},
				BrowseLocalDir: func(ctx context.Context, path, cursor string, limit int) (map[string]any, error) {
					return browseLocalDir(ctx, path, cursor, limit, cfg.PilotRoot)
				},
				ApplySelection: func(ctx context.Context, revisionID string, sourceRoots, sensitiveConsents []string) (map[string]any, error) {
					return applySelection(ctx, revisionID, sourceRoots, sensitiveConsents, filepath.Join(cfg.PilotRoot, "config", "pilot.json"))
				},
				BuildCatalog: buildCatalogFunc(eng, ctrlClient, cfg.Repository, cfg.PasswordRef),
				OnAgentPin: func(wantSHA256 string, qualified bool) {
					exePath, err := os.Executable()
					if err != nil {
						return
					}
					// OnAgentPin only fires after a successful heartbeat, which is
					// exactly the proof a just-installed build needs to stop the
					// boot-loop guard from rolling it back.
					confirmUpgradeHealthy(exePath)
					if busy.Load() > 0 || upgradeRestartPending.Load() {
						return // try again on a later heartbeat, never mid-backup/restore
					}
					currentSHA, err := hashFile(exePath)
					if err != nil {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
					defer cancel()
					rollback, err := runAgentUpgrade(ctx, ctrlClient, eng.Runner, exePath, currentSHA, wantSHA256, qualified)
					if err != nil {
						fmt.Fprintf(os.Stderr, "agent upgrade attempt failed: %s\n", domain.Redact(err.Error()))
						return
					}
					if rollback == "" {
						return // already on the pinned build, or nothing qualified to move to
					}
					// The new binary is already verified and staged at the live
					// path. This deliberately does NOT report a graceful Stop --
					// SCM's recovery policy (EnsureRestartOnFailure, above) only
					// fires on exactly that kind of abnormal termination, and
					// relaunching is how the already-swapped binary actually
					// starts running.
					if err := writeUpgradeMarker(exePath, rollback); err != nil {
						// Without the marker the boot-loop guard can't protect this
						// upgrade; undo the swap rather than run unguarded.
						_ = restoreRollback(exePath, rollback)
						fmt.Fprintf(os.Stderr, "agent upgrade aborted, could not record rollback marker: %s\n", domain.Redact(err.Error()))
						return
					}
					fmt.Fprintf(os.Stderr, "agent upgrade staged, restarting to apply (rollback kept at %s)\n", filepath.Base(rollback))
					upgradeRestartPending.Store(true)
					// A backup or restore may have started while the new
					// build downloaded; let it finish before restarting.
					go func() {
						for busy.Load() > 0 {
							time.Sleep(10 * time.Second)
						}
						os.Exit(1)
					}()
				},
			}
			// Best-effort, idempotent: makes sure this service is configured to
			// be restarted by SCM after the deliberate abnormal exit an
			// automatic upgrade swap performs below. Installs from before this
			// feature existed never had this set; every startup re-applies it
			// rather than relying solely on a future reinstall.
			if winsvc.IsServiceContext() {
				// Only the real SCM-hosted service may reconfigure the
				// StowlineBackup service; an interactive `service run` (a test or
				// a developer) must never touch the installed one.
				_ = winsvc.EnsureRestartOnFailure()
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := pushRepositoryPasswordToEscrow(ctx, ctrlClient, cfg.PasswordRef); err != nil {
					// Best-effort: the vault is a convenience backstop for a lost
					// PC, not a precondition for this backup working. A failure
					// here (control plane briefly unreachable at startup, e.g.)
					// self-heals on the next service restart.
					fmt.Fprintf(os.Stderr, "escrow push failed: %s\n", domain.Redact(err.Error()))
				}
			}()
		}
		if exec != nil && exec.Client != nil {
			pilotJSON := filepath.Join(cfg.PilotRoot, "config", "pilot.json")
			go syncSourceRootsAtStart(context.Background(), exec.Client.SyncSourceRoots, func() []string {
				return currentSourceRoots(pilotJSON, cfg.SourceRoots)
			}, 10, time.Minute)
		}
		startLocalUIServer(sharedProgress, eng, svcApp, exec, cfg, restoreWithAdmission)
		qual := &qualification.Runner{
			Dir:              filepath.Join(cfg.PilotRoot, "state", "qualification"),
			AllowedSource:    qualification.PilotSyntheticCorpus,
			ConfigSources:    cfg.SourceRoots,
			Repository:       cfg.Repository,
			VSSMode:          cfg.VSSMode,
			PasswordRef:      cfg.PasswordRef,
			DeviceID:         cfg.DeviceID,
			InstallationID:   cfg.InstallationID,
			PolicyRevisionID: pol.RevisionID,
			CacheDir:         cfg.CacheDir,
			Host:             cfg.DeviceID,
			FreeSpaceFloor:   pol.FreeSpaceFloorBytes,
			BandwidthKiBps:   pol.BandwidthKiBps,
			WorkloadBytes:    qualification.DefaultWorkloadBytes,
			Journal:          svcApp.Journal,
			AllowExecute: func() bool {
				ok, err := identity.IsLocalSystem()
				return err == nil && ok
			},
			InspectACL: qualification.RequireAdminSystemACL,
			Backup: func(ctx context.Context, req domain.BackupRequest) (domain.BackupResult, error) {
				req.Deadline = time.Now().Add(cfgDeadline())
				return svcApp.Backup(ctx, req)
			},
		}
		return winsvc.Run(&scheduleAdapter{ctrl: ctrl, exec: exec, qual: qual}, !winsvc.IsServiceContext())
	default:
		return fmt.Errorf("unknown service op")
	}
}

type scheduleAdapter struct {
	ctrl *scheduler.Controller
	exec *ctrlclient.Executor
	qual *qualification.Runner
}

// SetCommandDiagnosticSink is called by the Windows service host after its
// Event Log handle is ready. CommandDiagnostic.String contains only the
// fixed lifecycle marker, stage, server-issued id/kind, and normalized class.
func (s *scheduleAdapter) SetCommandDiagnosticSink(sink func(string)) {
	if s == nil || s.exec == nil {
		return
	}
	if sink == nil {
		s.exec.OnCommandEvent = nil
		return
	}
	s.exec.OnCommandEvent = func(event ctrlclient.CommandDiagnostic) {
		sink(event.String())
	}
}

func leaseStatus(lease *ctrlclient.AdmissionResult) string {
	if lease == nil {
		return domain.AdmissionControlUnavailable
	}
	if lease.Status == "" {
		return domain.AdmissionDenied
	}
	return lease.Status
}

func (s scheduleAdapter) RunOnce(ctx context.Context) error {
	op := "none"
	if s.ctrl != nil {
		op = s.ctrl.LastReason()
	}
	var pollErr error
	if s.exec != nil {
		pollErr = s.exec.Poll(ctx, op)
	}
	_, tickErr := s.ctrl.Tick(ctx)
	// Keep both outcomes observable. ReportTick understands joined errors
	// and applies its existing redaction/de-duplication to each one; a
	// scheduler error in the same minute must not hide a command ACK error.
	return errors.Join(pollErr, tickErr)
}

func (s scheduleAdapter) PollQualification(ctx context.Context) error {
	if s.qual == nil {
		return nil
	}
	return s.qual.Poll(ctx)
}

func cfgDeadline() time.Duration { return 8 * time.Hour }

func proveP0B() error {
	log := telemetry.New(os.Stdout)
	log.Info("prove", "p0b.start", "starting local restic primitive proof")
	if err := writeConfig(); err != nil {
		return err
	}
	cfg, eng, svc, err := load()
	if err != nil {
		return err
	}
	if _, err := corpus.Generate(cfg.SourceRoots[0]); err != nil {
		return err
	}
	manA, err := corpus.WriteManifest(cfg.SourceRoots[0])
	if err != nil {
		return err
	}
	ctx := context.Background()
	repoID, err := eng.Init(ctx, cfg.Repository, cfg.PasswordRef)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already") {
		id, err2 := eng.CatConfig(ctx, cfg.Repository, cfg.PasswordRef)
		if err2 != nil {
			return err
		}
		repoID = id
	}
	log.Info("prove", "p0b.init", "repository ready id_len="+fmt.Sprint(len(repoID)))

	runBackup := func(vss domain.VSSMode) (domain.BackupResult, error) {
		return svc.Backup(ctx, domain.BackupRequest{
			DeviceID:       cfg.DeviceID,
			InstallationID: cfg.InstallationID,
			Repository:     cfg.Repository,
			SourceRoots:    cfg.SourceRoots,
			VSSMode:        vss,
			PasswordRef:    cfg.PasswordRef,
			CacheDir:       cfg.CacheDir,
			Deadline:       time.Now().Add(2 * time.Hour),
			Host:           cfg.DeviceID,
		})
	}

	a, err := runBackup(domain.VSSDisabled)
	if err != nil {
		return err
	}
	log.Info("prove", "p0b.backupA", fmt.Sprintf("outcome=%s snapshot=%s data_added=%d", a.Outcome, a.SnapshotID, a.Summary.DataAdded))
	a2, err := runBackup(domain.VSSDisabled)
	if err != nil {
		return err
	}
	log.Info("prove", "p0b.backupA2", fmt.Sprintf("outcome=%s data_added=%d unmodified=%d", a2.Outcome, a2.Summary.DataAdded, a2.Summary.FilesUnmodified))
	if err := corpus.MutateForSnapshotB(cfg.SourceRoots[0]); err != nil {
		return err
	}
	manB, err := corpus.WriteManifest(cfg.SourceRoots[0])
	if err != nil {
		return err
	}
	b, err := runBackup(domain.VSSDisabled)
	if err != nil {
		return err
	}
	log.Info("prove", "p0b.backupB", fmt.Sprintf("outcome=%s snapshot=%s", b.Outcome, b.SnapshotID))
	snaps, err := eng.Snapshots(ctx, cfg.Repository, cfg.PasswordRef)
	if err != nil {
		return err
	}
	chk, err := eng.Check(ctx, cfg.Repository, cfg.PasswordRef, true)
	if err != nil {
		return err
	}
	log.Info("prove", "p0b.check", fmt.Sprintf("ok=%v exit=%d", chk.OK, chk.ExitCode))

	runStamp := time.Now().UTC().Format("20060102T150405Z")
	restoreOne := func(id domain.SnapshotID, job string, expected *corpus.Manifest) error {
		full := fullSnap(snaps, id)
		res, err := svc.Restore(ctx, domain.RestoreRequest{
			// Unique per run so the proof is re-runnable against a persistent journal.
			JobID:         domain.JobID(job + "-" + runStamp),
			SnapshotID:    full,
			StagingRoot:   cfg.StagingRoot,
			PasswordRef:   cfg.PasswordRef,
			Repository:    cfg.Repository,
			VerifyContent: true,
			Deadline:      time.Now().Add(2 * time.Hour),
		})
		if err != nil {
			return err
		}
		if res.State != domain.RestoreReady {
			return fmt.Errorf("restore not ready: %s %s", res.State, res.ErrorMessage)
		}
		n, err := corpus.VerifyRestored(res.Destination, expected)
		if err != nil {
			return err
		}
		log.Info("prove", "p0b.restore_checksum", fmt.Sprintf("job=%s matched=%d dest=%s", job, n, res.Destination))
		return nil
	}
	if a.SnapshotID == "" || b.SnapshotID == "" {
		return fmt.Errorf("missing snapshot ids A=%s B=%s outcomes A=%s B=%s", a.SnapshotID, b.SnapshotID, a.Outcome, b.Outcome)
	}
	if err := restoreOne(fullSnap(snaps, a.SnapshotID), "restore-A", manA); err != nil {
		return err
	}
	if err := restoreOne(fullSnap(snaps, b.SnapshotID), "restore-B", manB); err != nil {
		return err
	}

	del := filepath.Join(cfg.SourceRoots[0], "TEST-DELETE-ME.txt")
	if err := os.Remove(del); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := restoreOne(fullSnap(snaps, a.SnapshotID), "restore-delete-recovery", manA); err != nil {
		return err
	}

	_, bad := eng.CatConfig(ctx, cfg.Repository, domain.SecretRef{Purpose: domain.SecretResticPassword, Locator: cfg.PasswordRef.Locator + ".missing"})
	log.Info("prove", "p0b.expected_failure", fmt.Sprintf("wrong/missing password produced error: %v", bad != nil))

	evidence := map[string]any{
		"repo_id_len":      len(repoID),
		"snapshot_a":       a.SnapshotID,
		"snapshot_a2":      a2.SnapshotID,
		"snapshot_b":       b.SnapshotID,
		"a_data_added":     a.Summary.DataAdded,
		"a2_data_added":    a2.Summary.DataAdded,
		"a2_files_unmod":   a2.Summary.FilesUnmodified,
		"snapshots":        len(snaps),
		"check_ok":         chk.OK,
		"check_exit":       chk.ExitCode,
		"a_outcome":        a.Outcome,
		"b_outcome":        b.Outcome,
		"expected_failure": bad != nil,
		"restore_checksum": "independent SHA-256 vs corpus manifest",
	}
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	out, err := writeRuntimeEvidence("p0b-local.json", eb)
	if err != nil {
		return err
	}
	log.Info("prove", "p0b.done", "wrote "+out)
	_ = manA
	_ = manB
	return nil
}

func fullSnap(snaps []ports.Snapshot, id domain.SnapshotID) domain.SnapshotID {
	want := strings.ToLower(string(id))
	for _, s := range snaps {
		got := strings.ToLower(string(s.ID))
		if got == want || strings.HasPrefix(got, want) || strings.HasPrefix(want, got) {
			return s.ID
		}
	}
	return id
}

func proveP0C() error {
	if err := writeConfig(); err != nil {
		return err
	}
	cfg, _, svc, err := load()
	if err != nil {
		return err
	}
	if _, err := corpus.Generate(cfg.SourceRoots[0]); err != nil {
		return err
	}
	ctx := context.Background()
	res, err := svc.Backup(ctx, domain.BackupRequest{
		DeviceID:       cfg.DeviceID,
		InstallationID: cfg.InstallationID,
		Repository:     cfg.Repository,
		SourceRoots:    cfg.SourceRoots,
		VSSMode:        domain.VSSRequired,
		PasswordRef:    cfg.PasswordRef,
		CacheDir:       cfg.CacheDir,
		Deadline:       time.Now().Add(2 * time.Hour),
		Host:           cfg.DeviceID,
	})
	if err != nil {
		return err
	}
	evidence := map[string]any{
		"outcome":             res.Outcome,
		"consistency":         res.Consistency,
		"error_class":         res.ErrorClass,
		"error_message":       res.ErrorMessage,
		"exit_code":           res.ExitCode,
		"exit_known":          res.ExitKnown,
		"vss_positive":        res.VSS.PositiveAllRequired,
		"vss_volumes":         res.VSS.Volumes,
		"vss_notes":           res.VSS.ParserNotes,
		"matched_lines":       res.VSS.RawMatchedLines,
		"snapshot_id":         res.SnapshotID,
		"published_not_green": res.PublishedNotGreen,
		"elevated":            identity.Elevated(),
		"note":                "Positive VSS evidence is required for CONSISTENCY_VERIFIED. Exit 0 is not sufficient. Restic 0.19.1 VSS success lines are stdout printer.P messages and are suppressed by --json. Unelevated runs are expected to fail closed.",
	}
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	out, werr := writeRuntimeEvidence("p0c-vss.json", eb)
	if werr != nil {
		return werr
	}
	fmt.Println(string(eb))
	fmt.Println("wrote", out)
	return nil
}

func proveRcloneLocal() error {
	if err := writeConfig(); err != nil {
		return err
	}
	cfg, _, _, err := load()
	if err != nil {
		return err
	}
	if _, err := corpus.Generate(cfg.SourceRoots[0]); err != nil {
		return err
	}
	confPath := filepath.Join(cfg.PilotRoot, "config", "rclone-local.conf")
	if err := os.WriteFile(confPath, []byte("[stowlinelocal]\ntype = local\n"), 0600); err != nil {
		return err
	}
	repoRel := "Repos/rclone-local"
	cfg.Repository = storage.RcloneLocalDescriptor("rclone:stowlinelocal:"+repoRel, confPath, cfg.Repository.GenerationID, cfg.DeviceID)
	j, err := journal.Open(filepath.Join(cfg.PilotRoot, "state", "rclone-local-journal.sqlite"))
	if err != nil {
		return err
	}
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		return err
	}
	eng := &restic.Adapter{
		Runner:     process.NewRunner(),
		Secrets:    prov,
		Connector:  storage.Connector{RclonePin: cfg.Binaries.Rclone},
		Pin:        cfg.Binaries.Restic,
		TempDir:    cfg.PilotRoot,
		CacheDir:   cfg.CacheDir,
		SystemRoot: os.Getenv("SystemRoot"),
		ExtraPath:  []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
	}
	svc := &application.Service{Engine: eng, Journal: j, Clock: ports.RealClock{}}
	ctx := context.Background()
	id, err := eng.Init(ctx, cfg.Repository, cfg.PasswordRef)
	if err != nil {
		id2, err2 := eng.CatConfig(ctx, cfg.Repository, cfg.PasswordRef)
		if err2 != nil {
			return fmt.Errorf("rclone-local init: %w", err)
		}
		id = id2
	}
	res, err := svc.Backup(ctx, domain.BackupRequest{
		DeviceID:       cfg.DeviceID,
		InstallationID: cfg.InstallationID,
		Repository:     cfg.Repository,
		SourceRoots:    cfg.SourceRoots,
		VSSMode:        domain.VSSDisabled,
		PasswordRef:    cfg.PasswordRef,
		CacheDir:       cfg.CacheDir,
		Deadline:       time.Now().Add(2 * time.Hour),
		Host:           cfg.DeviceID,
	})
	if err != nil {
		return err
	}
	snaps, _ := eng.Snapshots(ctx, cfg.Repository, cfg.PasswordRef)
	evidence := map[string]any{
		"repo_id":     id,
		"outcome":     res.Outcome,
		"snapshot_id": res.SnapshotID,
		"snapshots":   len(snaps),
		"error":       res.ErrorMessage,
		"transport":   "restic -> rclone serve restic --stdio -> local backend",
	}
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	out, werr := writeRuntimeEvidence("p0b-rclone-local.json", eb)
	if werr != nil {
		return werr
	}
	fmt.Println(string(eb))
	fmt.Println("wrote", out)
	if res.Outcome != domain.PhaseSucceeded && res.Outcome != domain.PhasePartial {
		return fmt.Errorf("rclone-local backup outcome %s", res.Outcome)
	}
	return nil
}

func cmdCanary(args []string) error {
	wantSnap := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--snapshot" && i+1 < len(args) {
			wantSnap = args[i+1]
			i++
		}
	}
	if wantSnap != "" {
		if len(wantSnap) != 64 {
			return fmt.Errorf("--snapshot must be a 64-hex snapshot id")
		}
		for _, c := range wantSnap {
			ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !ok {
				return fmt.Errorf("--snapshot must be a 64-hex snapshot id")
			}
		}
	}
	cfg, eng, svc, err := load()
	if err != nil {
		return err
	}
	snaps, err := eng.Snapshots(context.Background(), cfg.Repository, cfg.PasswordRef)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return fmt.Errorf("no snapshots to canary-restore")
	}
	latest := snaps[len(snaps)-1]
	if wantSnap != "" {
		found := false
		want := strings.ToLower(wantSnap)
		for _, s := range snaps {
			got := strings.ToLower(string(s.ID))
			if got == want || strings.HasPrefix(got, want) || strings.HasPrefix(want, got) {
				latest = s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("snapshot %s not in repository", wantSnap)
		}
	}
	expected, expErr := application.LoadExpectedManifest(filepath.Join(cfg.PilotRoot, "manifests"), string(latest.ID))
	evidence := map[string]any{
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"snapshot_id":     latest.ID,
		"do_not_execute":  true,
		"synthetic_pilot": true,
	}
	if expErr != nil {
		evidence["result"] = "UNVERIFIED"
		evidence["independent_result"] = "UNVERIFIED: no snapshot-specific expected manifest"
		evidence["independent_ok"] = false
		eb, _ := json.MarshalIndent(evidence, "", "  ")
		out, _ := writeRuntimeEvidence("canary.json", eb)
		fmt.Println(string(eb))
		if out != "" {
			fmt.Println("wrote", out)
		}
		return fmt.Errorf("canary UNVERIFIED: missing expected manifest for snapshot")
	}
	job := domain.JobID("canary-" + time.Now().UTC().Format("20060102T150405Z"))
	_, down, limErr := domain.CLIWANLimits(cfg.Repository.BackendKind, cfg.Repository.Capabilities.QualificationOnly)
	if limErr != nil {
		return limErr
	}
	res, err := svc.Restore(context.Background(), domain.RestoreRequest{
		JobID:                job,
		SnapshotID:           latest.ID,
		StagingRoot:          cfg.StagingRoot,
		PasswordRef:          cfg.PasswordRef,
		Repository:           cfg.Repository,
		VerifyContent:        true,
		Deadline:             time.Now().Add(2 * time.Hour),
		RestoreDownloadKiBps: down,
		BandwidthKiBps:       down,
	})
	if err != nil {
		return err
	}
	evidence["state"] = res.State
	evidence["destination"] = res.Destination
	evidence["engine_verify"] = res.VerifiedByEngine
	evidence["expected_manifest_sha256"] = expected.ManifestSHA256
	independentOK := false
	if res.State == domain.RestoreReady {
		man := &corpus.Manifest{Algorithm: "SHA-256", Files: expected.Files}
		matched, cErr := corpus.VerifyRestored(res.Destination, man)
		evidence["independent_matched"] = matched
		if cErr != nil {
			evidence["independent_result"] = "MISMATCH: " + cErr.Error()
			evidence["result"] = "FAIL"
		} else {
			independentOK = true
			evidence["independent_result"] = "OK"
			evidence["result"] = "READY"
		}
	} else {
		evidence["result"] = "FAIL"
		evidence["independent_result"] = "restore not READY"
	}
	evidence["independent_ok"] = independentOK
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	if out, werr := writeRuntimeEvidence("canary.json", eb); werr == nil {
		fmt.Println("wrote", out)
	}
	fmt.Println(string(eb))
	if res.State != domain.RestoreReady || !independentOK {
		return fmt.Errorf("canary did not fully verify snapshot %s", latest.ID)
	}
	return nil
}

func proveCancel() error {
	if err := writeConfig(); err != nil {
		return err
	}
	cfg, _, _, err := load()
	if err != nil {
		return err
	}
	src := filepath.Join(cfg.PilotRoot, "TestCorpus-cancel")
	if err := os.MkdirAll(src, 0755); err != nil {
		return err
	}
	blob := filepath.Join(src, "TEST-LARGE.bin")
	if st, err := os.Stat(blob); err != nil || st.Size() < 32*1024*1024 {
		f, err := os.Create(blob)
		if err != nil {
			return err
		}
		buf := make([]byte, 1024*1024)
		for i := 0; i < 48; i++ {
			if _, err := rand.Read(buf); err != nil {
				_ = f.Close()
				return err
			}
			if _, err := f.Write(buf); err != nil {
				_ = f.Close()
				return err
			}
		}
		_ = f.Close()
	}
	repo := cfg.Repository
	repo.Location = filepath.Join(cfg.PilotRoot, "Repos", "cancel-test")
	_ = os.MkdirAll(repo.Location, 0700)
	j, err := journal.Open(filepath.Join(cfg.PilotRoot, "state", "cancel-journal.sqlite"))
	if err != nil {
		return err
	}
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		return err
	}
	eng := &restic.Adapter{
		Runner:     process.NewRunner(),
		Secrets:    prov,
		Connector:  storage.Connector{RclonePin: cfg.Binaries.Rclone},
		Pin:        cfg.Binaries.Restic,
		TempDir:    cfg.TempDir,
		CacheDir:   cfg.CacheDir,
		SystemRoot: os.Getenv("SystemRoot"),
		ExtraPath:  []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
	}
	svc := &application.Service{Engine: eng, Journal: j, Clock: ports.RealClock{}}
	ctxInit := context.Background()
	if _, err := eng.Init(ctxInit, repo, cfg.PasswordRef); err != nil {
		if _, err2 := eng.CatConfig(ctxInit, repo, cfg.PasswordRef); err2 != nil {
			return fmt.Errorf("cancel-test init: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	res, err := svc.Backup(ctx, domain.BackupRequest{
		DeviceID:       cfg.DeviceID,
		InstallationID: cfg.InstallationID,
		Repository:     repo,
		SourceRoots:    []string{src},
		VSSMode:        domain.VSSDisabled,
		PasswordRef:    cfg.PasswordRef,
		CacheDir:       cfg.CacheDir,
		Deadline:       time.Now().Add(2 * time.Hour),
		Host:           cfg.DeviceID,
	})
	if err != nil && res.Outcome == "" {
		return err
	}
	recon, _ := svc.Reconcile(context.Background(), repo, cfg.PasswordRef)
	falseGreen := res.Outcome == domain.PhaseSucceeded
	evidence := map[string]any{
		"outcome":            res.Outcome,
		"error_class":        res.ErrorClass,
		"error_message":      res.ErrorMessage,
		"snapshot_id":        res.SnapshotID,
		"false_green":        falseGreen,
		"reconcile_attempts": len(recon),
		"timeout_ms":         400,
		"source":             src,
		"note":               "Synthetic cancel corpus only. SUCCEEDED after a 400ms deadline is recorded honestly if Restic finished first.",
	}
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	out, werr := writeRuntimeEvidence("p0-cancel.json", eb)
	if werr != nil {
		return werr
	}
	fmt.Println(string(eb))
	fmt.Println("wrote", out)
	if falseGreen {
		return fmt.Errorf("cancel proof: backup completed before timeout; rerun with a larger corpus")
	}
	return nil
}
