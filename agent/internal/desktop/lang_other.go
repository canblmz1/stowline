//go:build !windows

package desktop

import "os"

func detectTurkish() bool {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return isTurkishTag(v)
		}
	}
	return false
}
