package desktop

import "strings"

// Turkish is true when the Windows display language is Turkish; every other
// language gets English. Tests may set it directly.
var Turkish = detectTurkish()

// T picks the Turkish or English text for what the desktop programs show
// (window pages, dialogs, folder names), following the Windows display
// language. Web pages use the shared i18n.js layer instead.
func T(tr, en string) string {
	if Turkish {
		return tr
	}
	return en
}

func isTurkishTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	return tag == "tr" || strings.HasPrefix(tag, "tr-") || strings.HasPrefix(tag, "tr_")
}
