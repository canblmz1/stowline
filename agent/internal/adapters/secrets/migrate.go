package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const (
	UserEnvelopeName    = "restic-password.user.dpapi"
	MachineEnvelopeName = "restic-password.machine.dpapi"
	machineTmpName      = "restic-password.machine.dpapi.tmp"
	LegacyPasswordName  = "restic-password"
)

// AuthenticateFunc proves a SecretRef unlocks the configured repository.
// Implementations must use the Restic process boundary (no password on argv).
type AuthenticateFunc func(ctx context.Context, ref domain.SecretRef) (repoID string, err error)

// OpenFunc decrypts a secret handle. Callers must Close the handle.
type OpenFunc func(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error)

// ProtectFunc writes a named envelope under the machine-scope provider.
type ProtectFunc func(name string, plaintext []byte) error

// GenerateFunc returns a new repository password. It must not be used when a
// repository already exists.
type GenerateFunc func() ([]byte, error)

// MigrateRequest re-wraps an existing repository password into machine-scope DPAPI.
type MigrateRequest struct {
	Dir            string
	RepoLocation   string
	Authenticate   AuthenticateFunc
	Open           OpenFunc
	ProtectMachine ProtectFunc
	Generate       GenerateFunc
}

// UserRef is the interactive/user-scope envelope locator.
func UserRef(dir string) domain.SecretRef {
	return domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderDPAPIUser,
		Locator:  filepath.Join(dir, UserEnvelopeName),
	}
}

// MachineRef is the LocalSystem/machine-scope envelope locator.
func MachineRef(dir string) domain.SecretRef {
	return domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderDPAPIMachine,
		Locator:  filepath.Join(dir, MachineEnvelopeName),
	}
}

func machineTmpRef(dir string) domain.SecretRef {
	ref := MachineRef(dir)
	ref.Locator = filepath.Join(dir, machineTmpName)
	return ref
}

func legacyRef(dir string) domain.SecretRef {
	return domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderFile,
		Locator:  filepath.Join(dir, LegacyPasswordName),
	}
}

// RepoConfigExists reports whether a local Restic repository config file is present.
// Permission errors are treated as "exists" so a replacement password is not generated.
func RepoConfigExists(location string) bool {
	if location == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(location, "config"))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return true
}

// DefaultOpen selects the provider named by ref and opens it.
func DefaultOpen(ctx context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	p, _, err := ForRef(ref)
	if err != nil {
		return nil, err
	}
	return p.Open(ctx, ref)
}

// DefaultProtectMachine writes a machine-scope DPAPI envelope into dir.
func DefaultProtectMachine(dir string) ProtectFunc {
	p := NewService(dir)
	return func(name string, plaintext []byte) error {
		return p.Protect(name, plaintext)
	}
}

// DefaultGenerate returns a 32-byte hex password for an uninitialized repository.
func DefaultGenerate() ([]byte, error) {
	pw := make([]byte, 32)
	if _, err := rand.Read(pw); err != nil {
		return nil, err
	}
	hexed := hex.EncodeToString(pw)
	zero(pw)
	return []byte(hexed), nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func cloneSecret(h ports.SecretHandle) []byte {
	if h == nil {
		return nil
	}
	src := h.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// ReWrapToMachine copies an existing repository password into a machine-scope
// envelope. It never generates a replacement password when the repository already
// exists. A failure leaves the previous usable source intact.
func ReWrapToMachine(ctx context.Context, req MigrateRequest) (domain.SecretRef, error) {
	if req.Dir == "" {
		return domain.SecretRef{}, fmt.Errorf("%w: secrets directory required", domain.ErrConfig)
	}
	if req.Authenticate == nil {
		return domain.SecretRef{}, fmt.Errorf("%w: repository authenticator required", domain.ErrConfig)
	}
	open := req.Open
	if open == nil {
		open = DefaultOpen
	}
	protect := req.ProtectMachine
	if protect == nil {
		protect = DefaultProtectMachine(req.Dir)
	}
	generate := req.Generate
	if generate == nil {
		generate = DefaultGenerate
	}
	if err := os.MkdirAll(req.Dir, 0700); err != nil {
		return domain.SecretRef{}, err
	}

	dest := MachineRef(req.Dir)
	repoExists := RepoConfigExists(req.RepoLocation)

	if filePresent(dest.Locator) {
		if _, err := req.Authenticate(ctx, dest); err == nil {
			return dest, nil
		}
		if !repoExists {
			return dest, fmt.Errorf("%w: existing machine envelope does not authenticate and no repository is present to re-init silently", domain.ErrAuth)
		}
	}

	secret, srcRef, err := loadSourceSecret(ctx, req.Dir, open)
	if err != nil {
		if repoExists {
			return domain.SecretRef{}, fmt.Errorf("%w: existing repository; refusing to generate a replacement password (%v)", domain.ErrConfig, err)
		}
		secret, err = generate()
		if err != nil {
			return domain.SecretRef{}, err
		}
		srcRef = domain.SecretRef{}
	} else if repoExists {
		if _, err := req.Authenticate(ctx, srcRef); err != nil {
			zero(secret)
			if errors.Is(err, domain.ErrAuth) {
				return domain.SecretRef{}, fmt.Errorf("%w: source secret does not unlock the existing repository", domain.ErrAuth)
			}
			return domain.SecretRef{}, fmt.Errorf("source secret could not authenticate the existing repository: %w", err)
		}
	}

	tmp := machineTmpRef(req.Dir)
	_ = os.Remove(tmp.Locator)
	if err := protect(machineTmpName, secret); err != nil {
		zero(secret)
		return domain.SecretRef{}, err
	}

	back, err := open(ctx, tmp)
	if err != nil {
		zero(secret)
		_ = os.Remove(tmp.Locator)
		return domain.SecretRef{}, fmt.Errorf("machine envelope read-back failed: %w", err)
	}
	same := bytes.Equal(back.Bytes(), secret)
	_ = back.Close()
	if !same {
		zero(secret)
		_ = os.Remove(tmp.Locator)
		return domain.SecretRef{}, fmt.Errorf("%w: machine envelope read-back did not preserve secret bytes", domain.ErrSecretScopeMismatch)
	}

	if repoExists || srcRef.Locator != "" {
		if _, err := req.Authenticate(ctx, tmp); err != nil {
			zero(secret)
			_ = os.Remove(tmp.Locator)
			if errors.Is(err, domain.ErrAuth) {
				return domain.SecretRef{}, fmt.Errorf("%w: temporary machine envelope does not unlock the repository", domain.ErrAuth)
			}
			return domain.SecretRef{}, err
		}
	}
	zero(secret)

	bak, err := promoteEnvelope(req.Dir, machineTmpName, MachineEnvelopeName)
	if err != nil {
		_ = os.Remove(tmp.Locator)
		return domain.SecretRef{}, err
	}

	if repoExists || srcRef.Locator != "" {
		if _, err := req.Authenticate(ctx, dest); err != nil {
			if bak != "" {
				_ = os.Remove(dest.Locator)
				_ = os.Rename(bak, dest.Locator)
			}
			if errors.Is(err, domain.ErrAuth) {
				return domain.SecretRef{}, fmt.Errorf("%w: promoted machine envelope does not unlock the repository", domain.ErrAuth)
			}
			return domain.SecretRef{}, err
		}
	}
	if bak != "" {
		_ = os.Remove(bak)
	}
	return dest, nil
}

func filePresent(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadSourceSecret(ctx context.Context, dir string, open OpenFunc) ([]byte, domain.SecretRef, error) {
	candidates := []domain.SecretRef{UserRef(dir), legacyRef(dir)}
	var last error
	var sawPossibleSource bool
	for _, ref := range candidates {
		_, err := os.Stat(ref.Locator)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			sawPossibleSource = true
			last = err
			h, oerr := open(ctx, ref)
			if oerr != nil {
				last = oerr
				continue
			}
			secret := cloneSecret(h)
			_ = h.Close()
			if len(secret) == 0 {
				zero(secret)
				last = fmt.Errorf("empty secret")
				continue
			}
			return secret, ref, nil
		}
		sawPossibleSource = true
		h, err := open(ctx, ref)
		if err != nil {
			last = err
			continue
		}
		secret := cloneSecret(h)
		_ = h.Close()
		if len(secret) == 0 {
			zero(secret)
			last = fmt.Errorf("empty secret")
			continue
		}
		return secret, ref, nil
	}
	if sawPossibleSource && last != nil {
		return nil, domain.SecretRef{}, last
	}
	return nil, domain.SecretRef{}, fmt.Errorf("no recoverable source secret")
}

func promoteEnvelope(dir, tmpName, finalName string) (bakPath string, err error) {
	tmp := filepath.Join(dir, tmpName)
	final := filepath.Join(dir, finalName)
	bak := filepath.Join(dir, finalName+".bak")
	if _, err := os.Stat(final); err == nil {
		_ = os.Remove(bak)
		if err := os.Rename(final, bak); err != nil {
			return "", err
		}
		bakPath = bak
	}
	if err := os.Rename(tmp, final); err != nil {
		if bakPath != "" {
			_ = os.Rename(bakPath, final)
		}
		return "", err
	}
	return bakPath, nil
}

// ReWrapNamedToMachine copies an existing user-scope envelope into destName as
// machine-scope. A missing source is a no-op. The user envelope is left in place.
func ReWrapNamedToMachine(ctx context.Context, dir, srcName, destName string) error {
	if dir == "" || srcName == "" || destName == "" {
		return fmt.Errorf("%w: re-wrap names required", domain.ErrConfig)
	}
	if filepath.Base(srcName) != srcName || filepath.Base(destName) != destName {
		return fmt.Errorf("%w: invalid envelope name", domain.ErrConfig)
	}
	src := filepath.Join(dir, srcName)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dest := filepath.Join(dir, destName)
	if filePresent(dest) {
		h, err := DefaultOpen(ctx, domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: dest})
		if err == nil {
			_ = h.Close()
			return nil
		}
	}
	h, err := DefaultOpen(ctx, domain.SecretRef{Provider: domain.SecretProviderDPAPIUser, Locator: src})
	if err != nil {
		return err
	}
	secret := cloneSecret(h)
	_ = h.Close()
	if err := DefaultProtectMachine(dir)(destName, secret); err != nil {
		zero(secret)
		return err
	}
	back, err := DefaultOpen(ctx, domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: dest})
	if err != nil {
		zero(secret)
		_ = os.Remove(dest)
		return err
	}
	same := bytes.Equal(back.Bytes(), secret)
	_ = back.Close()
	zero(secret)
	if !same {
		_ = os.Remove(dest)
		return fmt.Errorf("%w: machine envelope read-back did not preserve secret bytes", domain.ErrSecretScopeMismatch)
	}
	return nil
}
