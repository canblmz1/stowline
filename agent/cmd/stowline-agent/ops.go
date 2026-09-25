package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/canblmz1/stowline/agent/internal/enroll"
	"github.com/canblmz1/stowline/agent/internal/pilot/preflight"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/qualification"
	"github.com/canblmz1/stowline/agent/internal/scheduler"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
	"github.com/canblmz1/stowline/agent/internal/windows/identity"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

func load() (*config.File, *restic.Adapter, *application.Service, error) {
	return loadMode(false)
}

func loadMode(serviceMode bool) (*config.File, *restic.Adapter, *application.Service, error) {
	path := `C:\Stowline\config\pilot.json`
	cfg, err := config.Load(path)
	if err != nil {
		// Only a genuinely absent config may fall back to defaults. An
		// existing pilot.json that fails to parse or validate must stop the
		// agent: silently running defaults backs up a different folder than
		// the one configured and nobody notices.
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, fmt.Errorf("%w: %s is unreadable: %v", domain.ErrConfig, path, err)
		}
		d := config.DefaultPilot()
		cfg = &d
	}
	if serviceMode {
		if err := secrets.RequireServiceProvider(cfg.PasswordRef); err != nil {
			return nil, nil, nil, err
		}
		if cfg.ControlSecretRef.Locator != "" {
			if err := secrets.RequireServiceProvider(cfg.ControlSecretRef); err != nil {
				return nil, nil, nil, err
			}
		}
		if cfg.Repository.CredentialRefs != nil {
			if ref, ok := cfg.Repository.CredentialRefs[domain.SecretRESTPassword]; ok && ref.Locator != "" {
				if err := secrets.RequireServiceProvider(ref); err != nil {
					return nil, nil, nil, err
				}
			}
		}
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(cfg.TempDir, 0700); err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.JournalPath), 0700); err != nil {
		return nil, nil, nil, err
	}
	j, err := journal.Open(cfg.JournalPath)
	if err != nil {
		return nil, nil, nil, err
	}
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		return nil, nil, nil, err
	}
	eng := &restic.Adapter{
		Runner:         process.NewRunner(),
		Secrets:        prov,
		Connector:      storage.Connector{RclonePin: cfg.Binaries.Rclone},
		Pin:            cfg.Binaries.Restic,
		TempDir:        cfg.TempDir,
		CacheDir:       cfg.CacheDir,
		SystemRoot:     os.Getenv("SystemRoot"),
		ExtraPath:      []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
		ForceTerminate: serviceMode,
		GracePeriod:    winsvc.ForcedKillAfter,
	}
	svc := &application.Service{
		Engine:      eng,
		Journal:     j,
		Clock:       ports.RealClock{},
		ManifestDir: filepath.Join(cfg.PilotRoot, "manifests"),
	}
	return cfg, eng, svc, nil
}

func provisionSecrets(cfg *config.File, mode string) error {
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	switch mode {
	case "pilot":
		loc := filepath.Join(dir, "restic-password.user.dpapi")
		cfg.PasswordRef = domain.SecretRef{
			Purpose:  domain.SecretResticPassword,
			Provider: domain.SecretProviderDPAPIUser,
			Locator:  loc,
		}
		if _, err := os.Stat(loc); os.IsNotExist(err) {
			pw, err := secretBytes(dir, secrets.RepoConfigExists(cfg.Repository.Location))
			if err != nil {
				return err
			}
			if err := secrets.NewPilot(dir).Protect(filepath.Base(loc), pw); err != nil {
				return err
			}
			zero(pw)
		}
		if err := acl.Apply(dir, acl.PilotWorking); err != nil {
			return fmt.Errorf("pilot secret ACL: %w", err)
		}
		_ = acl.Apply(filepath.Dir(cfg.JournalPath), acl.PilotWorking)
		_ = acl.Apply(cfg.StagingRoot, acl.RestoreStaging)
		_ = acl.Apply(filepath.Join(cfg.PilotRoot, "config"), acl.PilotWorking)
	case "service":
		ref, err := secrets.ReWrapToMachine(context.Background(), secrets.MigrateRequest{
			Dir:          dir,
			RepoLocation: cfg.Repository.Location,
			Authenticate: func(ctx context.Context, ref domain.SecretRef) (string, error) {
				id, err := authenticateRepository(cfg, ref)
				if err == nil && cfg.Repository.ExpectedRepoID == "" && id != "" {
					cfg.Repository.ExpectedRepoID = id
				}
				return id, err
			},
		})
		if err != nil {
			return err
		}
		cfg.PasswordRef = ref
		if err := secrets.ReWrapNamedToMachine(context.Background(), dir, "control-credential.user.dpapi", "control-credential.machine.dpapi"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "control-credential.machine.dpapi")); err == nil {
			cfg.ControlSecretRef = domain.SecretRef{
				Purpose:  "control-plane",
				Provider: domain.SecretProviderDPAPIMachine,
				Locator:  filepath.Join(dir, "control-credential.machine.dpapi"),
			}
		}
		if err := secrets.ReWrapNamedToMachine(context.Background(), dir, "gateway-credential.user.dpapi", "gateway-credential.machine.dpapi"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "gateway-credential.machine.dpapi")); err == nil {
			if cfg.Repository.CredentialRefs == nil {
				cfg.Repository.CredentialRefs = map[string]domain.SecretRef{}
			}
			cfg.Repository.CredentialRefs[domain.SecretRESTPassword] = domain.SecretRef{
				Purpose:  domain.SecretRESTPassword,
				Provider: domain.SecretProviderDPAPIMachine,
				Locator:  filepath.Join(dir, "gateway-credential.machine.dpapi"),
			}
		}
		if err := acl.Apply(dir, acl.ServiceSecret); err != nil {
			return fmt.Errorf("service secret ACL: %w", err)
		}
		_ = acl.Apply(filepath.Dir(cfg.JournalPath), acl.ServiceState)
		_ = acl.Apply(cfg.StagingRoot, acl.RestoreStaging)
	default:
		return fmt.Errorf("mode must be pilot or service")
	}
	return nil
}

func secretBytes(dir string, repoExists bool) ([]byte, error) {
	legacy := filepath.Join(dir, secrets.LegacyPasswordName)
	if b, err := os.ReadFile(legacy); err == nil {
		return []byte(strings.TrimSpace(string(b))), nil
	}
	if repoExists {
		return nil, fmt.Errorf("%w: existing repository; refusing to generate a replacement password", domain.ErrConfig)
	}
	return secrets.DefaultGenerate()
}

func authenticateRepository(cfg *config.File, ref domain.SecretRef) (string, error) {
	prov, _, err := secrets.ForRef(ref)
	if err != nil {
		return "", err
	}
	if cfg.TempDir != "" {
		if err := os.MkdirAll(cfg.TempDir, 0700); err != nil {
			return "", err
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return eng.CatConfig(ctx, cfg.Repository, ref)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func cmdSecrets(args []string) error {
	if len(args) < 1 || args[0] != "provision" {
		return fmt.Errorf("usage: stowline-agent secrets provision --mode=pilot|service")
	}
	mode := "pilot"
	for i := 1; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--mode=") {
			mode = strings.TrimPrefix(args[i], "--mode=")
		}
		if args[i] == "--mode" && i+1 < len(args) {
			mode = args[i+1]
		}
	}
	cfg, err := config.Load(`C:\Stowline\config\pilot.json`)
	if err != nil {
		d := config.DefaultPilot()
		cfg = &d
	}
	if err := provisionSecrets(cfg, mode); err != nil {
		return err
	}
	return persistConfig(*cfg)
}

func persistConfig(cfg config.File) error {
	if err := os.MkdirAll(filepath.Join(cfg.PilotRoot, "config"), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.PilotRoot, "config", "pilot.json"), b, 0600)
}

func cmdPreflight(args []string) error {
	cfg, err := config.Load(`C:\Stowline\config\pilot.json`)
	if err != nil {
		d := config.DefaultPilot()
		cfg = &d
	}
	rep := preflight.Run(cfg)
	jsonPath := preflight.DefaultReportPath(cfg.PilotRoot)
	for i := 0; i < len(args); i++ {
		if args[i] == "--json" && i+1 < len(args) {
			jsonPath = args[i+1]
		}
	}
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0755); err != nil {
		jsonPath = filepath.Join(os.TempDir(), "stowline-preflight-last.json")
		_ = os.MkdirAll(filepath.Dir(jsonPath), 0755)
	}
	if err := rep.WriteJSON(jsonPath); err != nil {
		alt := filepath.Join(os.TempDir(), "stowline-preflight-last.json")
		if err2 := rep.WriteJSON(alt); err2 != nil {
			return err
		}
		jsonPath = alt
	}
	for _, c := range rep.Checks {
		fmt.Printf("%-24s %-18s %s\n", c.Name, c.Status, c.Detail)
	}
	fmt.Println("wrote", jsonPath)
	if rep.Unsuccessful() {
		return fmt.Errorf("preflight unsuccessful: FAIL or BLOCKED_EXTERNAL checks present")
	}
	return nil
}

func cmdStatus() error {
	cfg, _, svc, err := load()
	if err != nil {
		return err
	}
	pol := domain.DefaultPilotPolicy(cfg.SourceRoots)
	epoch := scheduler.ScheduleEpoch(pol.Schedule.Epoch, pol.RevisionID)
	due, key, slotErr := scheduler.NextSlot(cfg.DeviceID, cfg.InstallationID, epoch, pol.Schedule, time.Now())
	mode := "interactive"
	commandContext := "interactive CLI"
	if winsvc.IsServiceContext() {
		mode = "service"
		commandContext = "windows-service (SCM LocalSystem)"
	}
	st := map[string]any{
		"mode":                 mode,
		"command_context":      commandContext,
		"version":              "stowline-agent " + buildinfo.Version,
		"elevated":             identity.Elevated(),
		"source_roots":         cfg.SourceRoots,
		"policy_revision":      pol.RevisionID,
		"schedule_timezone":    pol.Schedule.Timezone,
		"schedule_window":      pol.Schedule.WindowStartHHMM + "-" + pol.Schedule.WindowEndHHMM,
		"scheduler_next_due":   due.UTC().Format(time.RFC3339),
		"scheduler_slot_key":   key,
		"vss_mode":             cfg.VSSMode,
		"repository_kind":      cfg.Repository.BackendKind,
		"secret_provider_kind": cfg.PasswordRef.Provider,
		"current_operation":    "none",
	}
	qualDir := filepath.Join(cfg.PilotRoot, "state", "qualification")
	st["qualification_mode"] = false
	if _, err := os.Stat(filepath.Join(qualDir, qualification.EnabledName)); err == nil {
		st["qualification_mode"] = true
	}
	if slotErr != nil {
		st["scheduler_error"] = slotErr.Error()
	}
	if svc != nil && svc.Journal != nil {
		if atts, err := svc.Journal.RecentAttempts(context.Background(), 20); err == nil {
			var last, lastOK, lastBad, lastQual *ports.AttemptRecord
			for i := range atts {
				a := atts[i]
				if last == nil {
					last = &a
				}
				if a.Kind.IsBackupWork() && a.Outcome == domain.PhaseSucceeded && lastOK == nil && a.Kind == domain.JobBackup {
					lastOK = &a
				}
				if a.Kind == domain.JobQualificationBackup && a.Outcome == domain.PhaseSucceeded && lastQual == nil {
					lastQual = &a
				}
				if a.Kind.IsBackupWork() && (a.Outcome == domain.PhaseFailed || a.Outcome == domain.PhasePartial) && lastBad == nil && a.Kind == domain.JobBackup {
					lastBad = &a
				}
				if a.Kind.IsBackupWork() && (a.Phase == domain.PhaseBackingUp || a.Phase == domain.PhasePreparing || a.Phase == domain.PhaseVSSPreparing) {
					st["current_operation"] = "backup"
				}
			}
			if last != nil {
				attemptClass := domain.AttemptScheduled
				if last.Kind == domain.JobQualificationBackup {
					attemptClass = domain.AttemptQualification
				}
				st["last_attempt"] = map[string]any{
					"phase":         last.Phase,
					"outcome":       last.Outcome,
					"error_class":   last.ErrorClass,
					"ended_at":      last.EndedAt,
					"kind":          last.Kind,
					"slot_key":      last.SlotKey,
					"snapshot_id":   last.SnapshotID,
					"attempt_class": attemptClass,
				}
			}
			if lastOK != nil {
				st["last_successful_backup"] = map[string]any{"outcome": lastOK.Outcome, "snapshot_id": lastOK.SnapshotID, "ended_at": lastOK.EndedAt, "slot_key": lastOK.SlotKey}
			}
			if lastQual != nil {
				st["last_qualification_backup"] = map[string]any{"outcome": lastQual.Outcome, "snapshot_id": lastQual.SnapshotID, "ended_at": lastQual.EndedAt, "slot_key": lastQual.SlotKey}
			}
			if lastBad != nil {
				st["last_failed_or_partial"] = map[string]any{"outcome": lastBad.Outcome, "error_class": lastBad.ErrorClass, "ended_at": lastBad.EndedAt}
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

func cmdControl(args []string) error {
	if len(args) < 1 || args[0] != "enroll" {
		return fmt.Errorf("usage: stowline-agent control enroll --url <control-plane> --token <token> [--mode=pilot|service]")
	}
	url, token, mode := "", "", enroll.ModePilot
	for i := 1; i < len(args); i++ {
		if args[i] == "--url" && i+1 < len(args) {
			url = args[i+1]
			i++
			continue
		}
		if args[i] == "--token" && i+1 < len(args) {
			token = args[i+1]
			i++
			continue
		}
		if args[i] == "--mode" && i+1 < len(args) {
			mode = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--mode=") {
			mode = strings.TrimPrefix(args[i], "--mode=")
		}
	}
	if url == "" || token == "" {
		return fmt.Errorf("usage: stowline-agent control enroll --url <control-plane> --token <token> [--mode=pilot|service]")
	}
	cfg, err := config.Load(`C:\Stowline\config\pilot.json`)
	if err != nil {
		d := config.DefaultPilot()
		cfg = &d
	}
	host, _ := os.Hostname()
	res, err := enroll.Run(context.Background(), enroll.Request{
		Mode:           mode,
		URL:            url,
		Token:          token,
		Hostname:       host,
		Version:        buildinfo.Version,
		InstallationID: cfg.InstallationID,
		Cfg:            cfg,
		Client:         &ctrlclient.Client{BaseURL: url},
		PersistConfig:  persistConfig,
	})
	if err != nil {
		return err
	}
	fmt.Println(enroll.DiagnosticString(res))
	return nil
}
