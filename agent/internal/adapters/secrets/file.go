package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

type memoryHandle struct {
	mu   sync.Mutex
	data []byte
}

func (h *memoryHandle) Bytes() []byte { return h.data }

func (h *memoryHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.data {
		h.data[i] = 0
	}
	h.data = nil
	return nil
}

// FileProvider reads a secret from a local file path stored in SecretRef.Locator.
// It never logs the contents.
type FileProvider struct{}

func (FileProvider) Kind() string { return domain.SecretProviderFile }

func (FileProvider) Open(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	_ = ctx
	if ref.Locator == "" {
		return nil, fmt.Errorf("%w: empty secret locator", domain.ErrConfig)
	}
	if strings.Contains(ref.Locator, "://") {
		return nil, domain.ErrRemoteExecutable
	}
	if strings.Contains(strings.ToLower(ref.Locator), ".dpapi") {
		return nil, fmt.Errorf("%w: file provider refuses DPAPI locators", domain.ErrSecretScopeMismatch)
	}
	b, err := os.ReadFile(ref.Locator)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(b, []byte("STOWLINE-DPAPI-1")) {
		return nil, fmt.Errorf("%w: file provider refuses DPAPI envelopes", domain.ErrSecretScopeMismatch)
	}
	val := strings.TrimSpace(string(b))
	return &memoryHandle{data: []byte(val)}, nil
}

// StaticProvider is for tests only.
type StaticProvider struct {
	Values map[string]string
}

func (StaticProvider) Kind() string { return "static-test" }

func (p StaticProvider) Open(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	_ = ctx
	v, ok := p.Values[ref.Locator]
	if !ok {
		v, ok = p.Values[ref.Purpose]
	}
	if !ok {
		return nil, fmt.Errorf("%w: secret not found", domain.ErrConfig)
	}
	return &memoryHandle{data: []byte(v)}, nil
}
