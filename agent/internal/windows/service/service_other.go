//go:build !windows

package service

import (
	"fmt"
	"time"
)

const (
	ShutdownBudget  = 60 * time.Second
	ForcedKillAfter = 2 * time.Second
)

func Run(start Startup, interactive bool) error {
	return fmt.Errorf("windows service is only available on Windows")
}

func IsServiceContext() bool   { return false }
func Install(exe string) error { return fmt.Errorf("windows only") }
func Remove() error            { return fmt.Errorf("windows only") }
func Control(cmd string) error { return fmt.Errorf("windows only") }
func QueryInstalled() (bool, string, error) {
	return false, "", fmt.Errorf("windows only")
}

func EnsureRestartOnFailure() error { return fmt.Errorf("windows only") }
