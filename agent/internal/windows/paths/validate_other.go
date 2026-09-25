//go:build !windows

package paths

import "github.com/canblmz1/stowline/agent/internal/domain"

func confinedOS(rootAbs, destAbs string) error {
	_ = rootAbs
	_ = destAbs
	return nil
}

func validateRootOS(root string) error {
	if IsForbiddenTarget(root) {
		return domain.ErrRestorePathRejected
	}
	return nil
}

func IsReparse(path string) bool          { return false }
func IsCloudPlaceholder(path string) bool { return false }
