// Command workspace-repo-init initializes the repository described by the
// current pilot.json using the same engine as stowline-agent. It prints the
// repository id prefix only.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

func main() {
	cfgPath := `C:\Stowline\config\pilot.json`
	if v := os.Getenv("STOWLINE_PILOT_JSON"); v != "" {
		cfgPath = v
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config failed")
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "cache dir")
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.TempDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "temp dir")
		os.Exit(1)
	}
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "secret provider")
		os.Exit(1)
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
		ForceTerminate: false,
		GracePeriod:    winsvc.ForcedKillAfter,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	id, err := eng.Init(ctx, cfg.Repository, cfg.PasswordRef)
	if err != nil {
		id2, err2 := eng.CatConfig(ctx, cfg.Repository, cfg.PasswordRef)
		if err2 != nil {
			fmt.Fprintln(os.Stderr, "init failed: "+domain.Redact(err.Error()))
			os.Exit(1)
		}
		id = id2
	}
	if len(id) > 12 {
		id = id[:12]
	}
	fmt.Println(id)
}
