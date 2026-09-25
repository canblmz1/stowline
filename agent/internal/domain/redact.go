package domain

import (
	"regexp"
	"strings"
)

var (
	hex64        = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	bearerRE     = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._\-+/=]+`)
	passwordRE   = regexp.MustCompile(`(?i)(password\s*[=:]\s*)\S+`)
	resticPassRE = regexp.MustCompile(`(?i)RESTIC_[A-Z_]*PASSWORD[A-Z_]*\s*=\s*\S+`)
	rclonePassRE = regexp.MustCompile(`(?i)RCLONE_CONFIG_PASS\s*=\s*\S+`)
	authRE       = regexp.MustCompile(`(?i)(authorization:\s*)\S+`)
)

// Redact removes secret material from a diagnostic string. It never logs complete environments.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	out = strings.ReplaceAll(out, CanarySecret, "[REDACTED]")
	out = resticPassRE.ReplaceAllString(out, "RESTIC_PASSWORD=[REDACTED]")
	out = rclonePassRE.ReplaceAllString(out, "RCLONE_CONFIG_PASS=[REDACTED]")
	out = bearerRE.ReplaceAllString(out, "${1}[REDACTED]")
	out = passwordRE.ReplaceAllString(out, "${1}[REDACTED]")
	out = authRE.ReplaceAllString(out, "${1}[REDACTED]")
	return out
}

// RedactArgs returns a copy of argv with secret-bearing tokens removed.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, a := range args {
		if hideNext {
			out[i] = "[REDACTED]"
			hideNext = false
			continue
		}
		al := strings.ToLower(a)
		if al == "--password" || al == "-p" || strings.HasPrefix(al, "--password-") {
			out[i] = a
			hideNext = true
			if strings.Contains(a, "=") {
				out[i] = stripKV(a)
				hideNext = false
			}
			continue
		}
		if strings.Contains(strings.ToLower(a), "password=") || strings.Contains(strings.ToLower(a), "secret=") {
			out[i] = stripKV(a)
			continue
		}
		out[i] = a
	}
	return out
}

func stripKV(a string) string {
	if i := strings.Index(a, "="); i >= 0 {
		return a[:i+1] + "[REDACTED]"
	}
	return "[REDACTED]"
}

// ContainsSecret reports whether a buffer includes a known canary or password-shaped leak.
func ContainsSecret(s string, extra ...string) bool {
	if strings.Contains(s, CanarySecret) {
		return true
	}
	for _, e := range extra {
		if e != "" && strings.Contains(s, e) {
			return true
		}
	}
	return false
}

// SnapshotIDLooksPlausible is a format check, not proof of recoverability.
func SnapshotIDLooksPlausible(id string) bool {
	return hex64.MatchString(id) && len(id) == 64
}
