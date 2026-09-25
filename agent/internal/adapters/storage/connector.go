package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Connector translates typed repository descriptors into engine location + options.
// Domain/application code must not switch on Google Drive vs S3.
type Connector struct {
	RclonePin domain.BinaryPin
}

func (c Connector) Bind(ctx context.Context, d domain.RepositoryDescriptor) (ports.BoundRepository, error) {
	_ = ctx
	if err := d.Validate(); err != nil {
		return ports.BoundRepository{}, err
	}
	bound := ports.BoundRepository{Capabilities: d.Capabilities}
	switch d.BackendKind {
	case domain.BackendLocal:
		if !filepath.IsAbs(d.Location) {
			return ports.BoundRepository{}, fmt.Errorf("%w: LOCAL backend cannot use rclone location", domain.ErrConfig)
		}
		bound.Location = d.Location
	case domain.BackendPilotRcloneLocal, domain.BackendPilotRcloneDrive:
		prog := filepath.ToSlash(c.RclonePin.Path)
		// Restic parses -o as CSV (no quoting) and then runs the value through
		// SplitShellStrings (bare "\" and spaces are separators). A forward-slash
		// absolute path with no spaces is the only form that both survives and
		// forces Go's exec to use the pinned binary with no PATH/CWD lookup.
		if !filepath.IsAbs(c.RclonePin.Path) || c.RclonePin.SHA256 == "" ||
			strings.ContainsAny(c.RclonePin.Path, "\"\r\n\x00,") || strings.ContainsAny(prog, " \t\\") {
			return ports.BoundRepository{}, fmt.Errorf("%w: rclone pin must be an absolute, digest-pinned path with no spaces", domain.ErrConfig)
		}
		if !strings.HasPrefix(d.Location, "rclone:") {
			return ports.BoundRepository{}, fmt.Errorf("%w: rclone backends require location rclone:<remote>:<path>", domain.ErrConfig)
		}
		bound.Location = d.Location
		bound.Options = []ports.KV{{Key: "rclone.program", Value: prog}}
		bound.BinaryPins = []domain.BinaryPin{c.RclonePin}
		if ref, ok := d.CredentialRefs["rclone-config"]; ok && ref.Locator != "" {
			bound.Env = append(bound.Env, ports.EnvVar{Name: "RCLONE_CONFIG", Value: ref.Locator, Secret: false})
		}
		if pass, ok := d.CredentialRefs[domain.SecretRcloneConfigPass]; ok && pass.Locator != "" {
			// Locator is a handle; actual password is injected by the engine via SecretProvider, not here.
			_ = pass
		}
	case domain.BackendRESTGateway:
		loc := d.Location
		if !(strings.HasPrefix(loc, "rest:https://") || strings.HasPrefix(loc, "rest:http://")) {
			return ports.BoundRepository{}, fmt.Errorf("%w: REST_GATEWAY location must be rest:https://host/path", domain.ErrConfig)
		}
		if strings.Contains(loc, "@") {
			return ports.BoundRepository{}, fmt.Errorf("%w: credentials must not appear in repository URLs", domain.ErrConfig)
		}
		httpLab := strings.HasPrefix(loc, "rest:http://")
		if httpLab && d.TransportProfile != "lab-insecure-http" {
			return ports.BoundRepository{}, fmt.Errorf("%w: HTTP REST gateway requires transport_profile=lab-insecure-http", domain.ErrTLSVerification)
		}
		if strings.Contains(loc, "..") {
			return ports.BoundRepository{}, fmt.Errorf("%w: gateway path rejected", domain.ErrConfig)
		}
		bound.Location = loc
	case domain.BackendS3, domain.BackendB2:
		return ports.BoundRepository{}, domain.ErrCapabilityUnsupported
	default:
		return ports.BoundRepository{}, fmt.Errorf("%w: unsupported backend", domain.ErrCapabilityUnsupported)
	}
	return bound, nil
}

func LocalDescriptor(path, generation, device string) domain.RepositoryDescriptor {
	abs := path
	if p, err := filepath.Abs(path); err == nil {
		abs = p
	}
	return domain.RepositoryDescriptor{
		BackendKind:      domain.BackendLocal,
		Location:         abs,
		GenerationID:     generation,
		DeviceID:         device,
		Capabilities:     domain.DefaultCapabilities(domain.BackendLocal),
		TransportProfile: "local-qualification",
	}
}

func RcloneLocalDescriptor(remotePath, rcloneConfig, generation, device string) domain.RepositoryDescriptor {
	d := domain.RepositoryDescriptor{
		BackendKind:      domain.BackendPilotRcloneLocal,
		Location:         remotePath,
		GenerationID:     generation,
		DeviceID:         device,
		Capabilities:     domain.DefaultCapabilities(domain.BackendPilotRcloneLocal),
		TransportProfile: "rclone-stdio-local",
		CredentialRefs:   map[string]domain.SecretRef{},
	}
	if rcloneConfig != "" {
		d.CredentialRefs["rclone-config"] = domain.SecretRef{Purpose: "rclone-config", Locator: rcloneConfig}
	}
	return d
}
