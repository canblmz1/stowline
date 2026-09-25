//go:build windows

package desktop

import "golang.org/x/sys/windows"

func detectTurkish() bool {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return err == nil && len(langs) > 0 && isTurkishTag(langs[0])
}
