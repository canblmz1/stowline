package qualification

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
)

func RequireAdminSystemACL(path string) error {
	ins, err := acl.Inspect(path)
	if err != nil {
		return err
	}
	ok, reason := acl.EvaluateServiceSecret(ins.ACEs)
	if !ok {
		return fmt.Errorf("%w: qualification ACL: %s", domain.ErrConfig, reason)
	}
	return nil
}

func SamePath(a, b string) bool {
	aa, err := filepath.Abs(filepath.Clean(a))
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(filepath.Clean(b))
	if err != nil {
		return false
	}
	return strings.EqualFold(aa, bb)
}

func BindSources(configured, allowed []string) ([]string, error) {
	if len(allowed) != 1 {
		return nil, fmt.Errorf("%w: qualification allows exactly one synthetic source", domain.ErrConfig)
	}
	if len(configured) != 1 {
		return nil, fmt.Errorf("%w: qualification requires exactly one configured source root", domain.ErrConfig)
	}
	if !SamePath(configured[0], allowed[0]) {
		return nil, fmt.Errorf("%w: qualification source is not the synthetic corpus", domain.ErrConfig)
	}
	return []string{filepath.Clean(configured[0])}, nil
}
