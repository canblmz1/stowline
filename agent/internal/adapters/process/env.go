package process

import (
	"fmt"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

var windowsSystemEnv = []string{
	"SystemRoot", "SYSTEMROOT", "SystemDrive", "SYSTEMDRIVE",
	"windir", "WINDIR", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// RestrictedEnv builds a child environment from an allowlist. Parent env is not copied.
func RestrictedEnv(extra []ports.EnvVar, cacheDir, tempDir, systemRoot string, extraPath []string) ([]ports.EnvVar, error) {
	out := make([]ports.EnvVar, 0, 32)
	seen := map[string]struct{}{}
	add := func(v ports.EnvVar) {
		if v.Name == "" {
			return
		}
		if _, ok := seen[strings.ToUpper(v.Name)]; ok {
			return
		}
		seen[strings.ToUpper(v.Name)] = struct{}{}
		out = append(out, v)
	}

	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	add(ports.EnvVar{Name: "SystemRoot", Value: systemRoot})
	add(ports.EnvVar{Name: "SYSTEMROOT", Value: systemRoot})
	add(ports.EnvVar{Name: "windir", Value: systemRoot})
	add(ports.EnvVar{Name: "WINDIR", Value: systemRoot})
	drive := systemRoot
	if len(drive) >= 2 && drive[1] == ':' {
		drive = drive[:2]
	} else {
		drive = "C:"
	}
	add(ports.EnvVar{Name: "SystemDrive", Value: drive})
	pathVal := systemRoot + `\System32`
	for _, d := range extraPath {
		if d == "" {
			continue
		}
		if strings.ContainsAny(d, ";\n\r") {
			return nil, fmt.Errorf("%w: invalid PATH element", domain.ErrArbitraryFlags)
		}
		pathVal += ";" + d
	}
	add(ports.EnvVar{Name: "PATH", Value: pathVal})
	if tempDir != "" {
		add(ports.EnvVar{Name: "TEMP", Value: tempDir})
		add(ports.EnvVar{Name: "TMP", Value: tempDir})
	}
	if cacheDir != "" {
		add(ports.EnvVar{Name: "RESTIC_CACHE_DIR", Value: cacheDir})
	}

	for _, e := range extra {
		upper := strings.ToUpper(e.Name)
		if upper == "RESTIC_PASSWORD_COMMAND" || upper == "RESTIC_FROM_PASSWORD_COMMAND" {
			return nil, fmt.Errorf("%w: password commands are forbidden", domain.ErrArbitraryFlags)
		}
		if upper == "COMSPEC" {
			return nil, domain.ErrShellInvocation
		}
		switch upper {
		// RESTIC_PROGRESS_FPS only sets how often restic prints its status
		// line; without it a service-hosted backup prints none at all.
		case "RESTIC_PASSWORD", "RESTIC_REST_USERNAME", "RESTIC_REST_PASSWORD", "RCLONE_CONFIG", "RCLONE_CONFIG_PASS", "RESTIC_PROGRESS_FPS":
		default:
			return nil, fmt.Errorf("%w: environment %s is not allowlisted", domain.ErrArbitraryFlags, e.Name)
		}
		add(e)
	}
	return out, nil
}

func envBlock(vars []ports.EnvVar) []string {
	lines := make([]string, 0, len(vars))
	for _, v := range vars {
		lines = append(lines, v.Name+"="+v.Value)
	}
	return lines
}
