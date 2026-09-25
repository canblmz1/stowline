package paths

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

var reservedNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

type Issue struct {
	Reason string
	Path   string
}

func (i Issue) Error() string {
	return i.Reason + ": " + i.Path
}

// ValidateRestoreDestination checks a candidate path before any restore write.
// String-prefix checks are not sufficient on their own; Windows-specific
// resolution is applied in validate_windows.go.
func ValidateRestoreDestination(stagingRoot, dest string) error {
	if dest == "" || stagingRoot == "" {
		return domain.ErrRestorePathRejected
	}
	if err := rejectUnsafeLexical(dest); err != nil {
		return err
	}
	if err := rejectUnsafeLexical(stagingRoot); err != nil {
		return err
	}
	return confinedToRoot(stagingRoot, dest)
}

func rejectUnsafeLexical(p string) error {
	if p == "" {
		return domain.ErrRestorePathRejected
	}
	if strings.Contains(p, "\x00") {
		return &Issue{Reason: "nul", Path: p}
	}
	if strings.Contains(p, "://") {
		return &Issue{Reason: "url", Path: p}
	}
	n := strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(n, `\\`) {
		return &Issue{Reason: "unc", Path: p}
	}
	if strings.HasPrefix(n, `\??\`) || strings.HasPrefix(strings.ToLower(n), `\device\`) {
		return &Issue{Reason: "device-namespace", Path: p}
	}
	if strings.HasPrefix(n, `\\?\`) {
		return &Issue{Reason: "extended-prefix", Path: p}
	}
	if strings.Contains(n, `:`) && !hasDrivePrefix(n) {
		return &Issue{Reason: "ads-or-colon", Path: p}
	}
	if hasDrivePrefix(n) && len(n) >= 2 && n[1] == ':' && (len(n) == 2 || (len(n) > 2 && n[2] != '\\' && n[2] != '/')) {
		return &Issue{Reason: "drive-relative", Path: p}
	}
	parts := strings.Split(n, `\`)
	for _, part := range parts {
		if part == ".." {
			return &Issue{Reason: "dotdot", Path: p}
		}
		base := part
		if i := strings.Index(base, "."); i >= 0 {
			base = base[:i]
		}
		if _, ok := reservedNames[strings.ToUpper(base)]; ok && base != "" {
			return &Issue{Reason: "reserved-name", Path: p}
		}
		if strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			if part != "." && part != ".." && part != "" {
				return &Issue{Reason: "trailing-dot-or-space", Path: p}
			}
		}
		for _, r := range part {
			if r < 32 || unicode.IsControl(r) {
				return &Issue{Reason: "control-char", Path: p}
			}
			if strings.ContainsRune(`<>"|?*`, r) {
				return &Issue{Reason: "illegal-char", Path: p}
			}
		}
	}
	return nil
}

func hasDrivePrefix(n string) bool {
	return len(n) >= 2 && unicode.IsLetter(rune(n[0])) && n[1] == ':'
}

func confinedToRoot(root, dest string) error {
	rAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	rAbs = filepath.Clean(rAbs)
	dAbs = filepath.Clean(dAbs)
	rel, err := filepath.Rel(rAbs, dAbs)
	if err != nil {
		return domain.ErrStagingEscape
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return domain.ErrStagingEscape
	}
	if filepath.IsAbs(rel) {
		return domain.ErrStagingEscape
	}
	return confinedOS(rAbs, dAbs)
}

func ForbiddenRestoreTargets() []string {
	return []string{
		`C:\Windows`,
		`C:\Program Files`,
		`C:\Program Files (x86)`,
		`C:\ProgramData\Stowline`,
		`C:\ProgramData\Microsoft\Windows\Start Menu\Programs\Startup`,
		`C:\Stowline\state`,
		`C:\Stowline\secrets`,
		`C:\Stowline\bin`,
	}
}

func NormalizeCompare(p string) string {
	n := strings.ReplaceAll(filepath.Clean(p), `/`, `\`)
	n = strings.TrimRight(n, `\`)
	return strings.ToLower(n)
}

func IsForbiddenTarget(dest string) bool {
	d := NormalizeCompare(dest)
	for _, f := range ForbiddenRestoreTargets() {
		fl := NormalizeCompare(f)
		if d == fl || strings.HasPrefix(d, fl+`\`) {
			return true
		}
	}
	return false
}

func ValidateStagingRoot(root string) error {
	if root == "" {
		return domain.ErrRestorePathRejected
	}
	if err := rejectUnsafeLexical(root); err != nil {
		return err
	}
	if IsForbiddenTarget(root) {
		return domain.ErrRestorePathRejected
	}
	return validateRootOS(root)
}
