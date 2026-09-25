package secrets

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestRequireServiceProviderNonWindows(t *testing.T) {
	if err := RequireServiceProvider(domain.SecretRef{Provider: domain.SecretProviderDPAPIUser}); err == nil {
		t.Fatal("expected failure")
	}
}
