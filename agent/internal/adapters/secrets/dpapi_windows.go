//go:build windows

package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const (
	magic                    = "STOWLINE-DPAPI-1"
	cryptProtectUIForbidden  = 0x1
	cryptProtectLocalMachine = 0x4
)

// DPAPIProvider encrypts with an explicit DPAPI scope. The scope is stored in
// the on-disk envelope so a user-scope blob can never be opened as machine-scope.
type DPAPIProvider struct {
	Dir   string
	Scope string // domain.SecretScopeUser or SecretScopeMachine
}

func NewPilot(dir string) DPAPIProvider {
	return DPAPIProvider{Dir: dir, Scope: domain.SecretScopeUser}
}

func NewService(dir string) DPAPIProvider {
	return DPAPIProvider{Dir: dir, Scope: domain.SecretScopeMachine}
}

func (p DPAPIProvider) Kind() string {
	if p.Scope == domain.SecretScopeMachine {
		return domain.SecretProviderDPAPIMachine
	}
	return domain.SecretProviderDPAPIUser
}

func (p DPAPIProvider) Protect(name string, plaintext []byte) error {
	if p.Scope != domain.SecretScopeUser && p.Scope != domain.SecretScopeMachine {
		return fmt.Errorf("%w: DPAPI scope must be user or machine", domain.ErrConfig)
	}
	if err := os.MkdirAll(p.Dir, 0700); err != nil {
		return err
	}
	blob, err := dpapiProtect(plaintext, p.Scope)
	if err != nil {
		return err
	}
	env, err := encodeEnvelope(p.Scope, blob)
	if err != nil {
		return err
	}
	path := filepath.Join(p.Dir, name)
	return os.WriteFile(path, env, 0600)
}

func (p DPAPIProvider) Open(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	_ = ctx
	if p.Scope != domain.SecretScopeUser && p.Scope != domain.SecretScopeMachine {
		return nil, fmt.Errorf("%w: DPAPI scope must be user or machine", domain.ErrConfig)
	}
	path := ref.Locator
	if path == "" {
		path = filepath.Join(p.Dir, ref.Purpose+".dpapi")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	scope, blob, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if scope != p.Scope {
		return nil, fmt.Errorf("%w: blob is %s, provider is %s", domain.ErrSecretScopeMismatch, scope, p.Scope)
	}
	plain, err := dpapiUnprotect(blob)
	if err != nil {
		return nil, err
	}
	return &memoryHandle{data: plain}, nil
}

func encodeEnvelope(scope string, blob []byte) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(magic)
	b.WriteByte('\n')
	b.WriteString("scope=" + scope + "\n")
	b.WriteByte('\n')
	b.Write(blob)
	return b.Bytes(), nil
}

// EnvelopeScope returns the on-disk DPAPI envelope scope without decrypting the secret.
func EnvelopeScope(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	scope, _, err := decodeEnvelope(raw)
	return scope, err
}

func decodeEnvelope(raw []byte) (scope string, blob []byte, err error) {
	parts := bytes.SplitN(raw, []byte("\n\n"), 2)
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("%w: missing DPAPI envelope (refusing raw/plaintext)", domain.ErrSecretScopeMismatch)
	}
	head := strings.Split(string(parts[0]), "\n")
	if len(head) < 2 || head[0] != magic || !strings.HasPrefix(head[1], "scope=") {
		return "", nil, fmt.Errorf("%w: invalid DPAPI envelope", domain.ErrSecretScopeMismatch)
	}
	scope = strings.TrimPrefix(head[1], "scope=")
	if scope != domain.SecretScopeUser && scope != domain.SecretScopeMachine {
		return "", nil, fmt.Errorf("%w: unknown envelope scope", domain.ErrSecretScopeMismatch)
	}
	if looksLikePlaintext(parts[1]) {
		return "", nil, fmt.Errorf("%w: envelope blob is not DPAPI", domain.ErrSecretScopeMismatch)
	}
	return scope, parts[1], nil
}

func looksLikePlaintext(blob []byte) bool {
	if len(blob) < 16 {
		return true
	}
	// DPAPI blobs start with a GUID version; reject obvious hex passwords.
	for _, c := range blob {
		if c < 9 || (c > 13 && c < 32) {
			return false
		}
	}
	return true
}

func dpapiProtect(data []byte, scope string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty secret")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	name, err := windows.UTF16PtrFromString("stowline")
	if err != nil {
		return nil, err
	}
	flags := uint32(cryptProtectUIForbidden)
	if scope == domain.SecretScopeMachine {
		flags |= cryptProtectLocalMachine
	}
	if err := windows.CryptProtectData(&in, name, nil, 0, nil, flags, &out); err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := unsafe.Slice(out.Data, out.Size)
	cp := make([]byte, len(buf))
	copy(cp, buf)
	return cp, nil
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty blob")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, cryptProtectUIForbidden, &out); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := unsafe.Slice(out.Data, out.Size)
	cp := make([]byte, len(buf))
	copy(cp, buf)
	return cp, nil
}
