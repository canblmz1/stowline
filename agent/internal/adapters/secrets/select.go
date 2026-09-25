package secrets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// ForRef returns the provider named by SecretRef. Unknown names fail closed.
func ForRef(ref domain.SecretRef) (ports.SecretProvider, string, error) {
	switch ref.Provider {
	case domain.SecretProviderFile, "":
		return FileProvider{}, domain.SecretProviderFile, nil
	case domain.SecretProviderDPAPIUser:
		return NewPilot(filepath.Dir(ref.Locator)), domain.SecretProviderDPAPIUser, nil
	case domain.SecretProviderDPAPIMachine:
		return NewService(filepath.Dir(ref.Locator)), domain.SecretProviderDPAPIMachine, nil
	default:
		return nil, "", fmt.Errorf("%w: unknown secret provider %q", domain.ErrConfig, ref.Provider)
	}
}

// RequireServiceProvider rejects interactive/user-scope and plaintext providers.
func RequireServiceProvider(ref domain.SecretRef) error {
	if ref.Provider != domain.SecretProviderDPAPIMachine {
		return fmt.Errorf("%w: service mode requires %s, not %s", domain.ErrSecretScopeMismatch, domain.SecretProviderDPAPIMachine, ref.Provider)
	}
	if strings.Contains(strings.ToLower(ref.Locator), ".user.") {
		return fmt.Errorf("%w: refusing user-scope locator in service mode", domain.ErrSecretScopeMismatch)
	}
	return nil
}
