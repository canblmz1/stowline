//go:build !windows

package secrets

import (
	"context"
	"fmt"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

type DPAPIProvider struct {
	Dir   string
	Scope string
}

func NewPilot(dir string) DPAPIProvider {
	return DPAPIProvider{Dir: dir, Scope: domain.SecretScopeUser}
}

func NewService(dir string) DPAPIProvider {
	return DPAPIProvider{Dir: dir, Scope: domain.SecretScopeMachine}
}

func (p DPAPIProvider) Kind() string { return "dpapi-" + p.Scope }

func (p DPAPIProvider) Protect(string, []byte) error {
	return fmt.Errorf("DPAPI is Windows-only")
}

func (p DPAPIProvider) Open(context.Context, domain.SecretRef) (ports.SecretHandle, error) {
	return nil, fmt.Errorf("DPAPI is Windows-only")
}

func EnvelopeScope(string) (string, error) {
	return "", fmt.Errorf("DPAPI is Windows-only")
}
