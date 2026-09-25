package process

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const maxCapture = 8 << 20 // 8 MiB per stream

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyExecutable(path, expectedSHA256 string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: empty executable path", domain.ErrBinaryDigestMismatch)
	}
	if strings.ContainsAny(path, "\n\r") {
		return fmt.Errorf("%w: executable path contains newlines", domain.ErrArbitraryFlags)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%w: executable path is a directory", domain.ErrBinaryDigestMismatch)
	}
	if expectedSHA256 == "" {
		return fmt.Errorf("%w: pinned digest is required", domain.ErrBinaryDigestMismatch)
	}
	sum, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, expectedSHA256) {
		return fmt.Errorf("%w: got %s want %s", domain.ErrBinaryDigestMismatch, sum, expectedSHA256)
	}
	return nil
}

func envNames(env []ports.EnvVar) []string {
	names := make([]string, 0, len(env))
	for _, e := range env {
		names = append(names, e.Name)
	}
	return names
}

func rejectShell(spec ports.Spec) error {
	base := strings.ToLower(spec.Executable)
	for _, forbidden := range []string{"cmd.exe", "cmd", "powershell.exe", "powershell", "pwsh.exe", "pwsh", "sh", "bash", "wscript.exe", "cscript.exe"} {
		if strings.HasSuffix(base, "\\"+forbidden) || strings.HasSuffix(base, "/"+forbidden) || strings.EqualFold(base, forbidden) {
			return domain.ErrShellInvocation
		}
	}
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "cmd.exe") || strings.Contains(joined, "powershell") {
		return domain.ErrShellInvocation
	}
	return nil
}

func rejectSecretArgs(spec ports.Spec, extraSecrets []string) error {
	for _, a := range spec.Args {
		if domain.ContainsSecret(a, extraSecrets...) {
			return domain.ErrSecretInArguments
		}
		al := strings.ToLower(a)
		if strings.HasPrefix(al, "--password=") || al == "--password" || al == "-p" {
			return domain.ErrSecretInArguments
		}
		if strings.Contains(al, "://") && strings.Contains(a, "@") {
			return domain.ErrSecretInArguments
		}
	}
	return nil
}

func secretValues(env []ports.EnvVar) []string {
	var out []string
	for _, e := range env {
		if e.Secret && e.Value != "" {
			out = append(out, e.Value)
		}
	}
	return out
}
