package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/canblmz1/stowline/agent/internal/config"
)

// currentSourceRoots re-reads source_roots from pilot.json at the start of
// every backup. APPLY_SELECTION (remote) and the local desktop page both
// rewrite pilot.json in place, but the service loads its config once at
// startup -- without this, a new folder selection silently had no effect
// until the service happened to restart. Falls back to the startup list if
// the file can't be read or yields no roots, so a transient read problem
// can never turn into a backup of nothing.
func currentSourceRoots(pilotJSONPath string, fallback []string) []string {
	raw, err := os.ReadFile(pilotJSONPath)
	if err != nil {
		return fallback
	}
	var data struct {
		SourceRoots []string `json:"source_roots"`
	}
	if err := json.Unmarshal(config.StripBOM(raw), &data); err != nil || len(data.SourceRoots) == 0 {
		return fallback
	}
	return data.SourceRoots
}

// syncSourceRootsAtStart tells the control plane which folders this PC
// backs up. Folders discovered at install are written only to pilot.json,
// so without this the admin panel showed "0 klasör" for a PC that was
// backing up fine. The server ignores an unchanged list and never lets a
// sync replace an admin selection still on its way to the PC. Retries
// because the service usually starts before the network is up.
func syncSourceRootsAtStart(ctx context.Context, report func(context.Context, []string) error, roots func() []string, attempts int, wait time.Duration) {
	for i := 0; i < attempts; i++ {
		current := roots()
		if len(current) == 0 {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := report(callCtx, current)
		cancel()
		if err == nil {
			return
		}
		if i == attempts-1 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// alwaysExcluded is kept out of every backup: an OST is Outlook's local
// cache of server mail, large and rebuilt by Outlook on its own.
const alwaysExcluded = "*.ost"

// currentExcludes re-reads exclude_paths from pilot.json at the start of
// every backup, like currentSourceRoots. Setup writes there the sensitive
// files (credentials, keys) under the chosen folders that the operator did
// not confirm, plus name patterns for ones created later -- folders are
// backed up as whole roots, so without these they would be included.
func currentExcludes(pilotJSONPath string) []string {
	out := []string{}
	if raw, err := os.ReadFile(pilotJSONPath); err == nil {
		var data struct {
			ExcludePaths []string `json:"exclude_paths"`
		}
		if json.Unmarshal(config.StripBOM(raw), &data) == nil {
			for _, p := range data.ExcludePaths {
				if p != "" && p[0] != '-' {
					out = append(out, p)
				}
			}
		}
	}
	for _, p := range out {
		if p == alwaysExcluded {
			return out
		}
	}
	return append(out, alwaysExcluded)
}
