package main

import (
	"path/filepath"
	"strings"
)

// sensitiveNames/sensitiveExts mirror server/app/discovery.py's
// SENSITIVE_NAMES/SENSITIVE_EXTS exactly -- the same consent gate
// app/main.py's apply_selection enforces for an admin-submitted selection
// applies here too for a locally self-service one, so a user cannot
// silently pick a folder that looks like a credential/key file as a
// backup source without an explicit acknowledgement either way.
var sensitiveNames = map[string]bool{
	".env": true, ".env.local": true, ".env.production": true,
	"credentials.json": true, "secrets.json": true,
	"id_rsa": true, "id_ed25519": true,
}

var sensitiveExts = map[string]bool{
	".pem": true, ".key": true, ".pfx": true, ".p12": true,
}

// isSensitivePath reports whether root itself (not its contents) looks
// like a credential or key file/folder name, the same test app/main.py's
// apply_selection runs against each requested source root.
func isSensitivePath(root string) bool {
	name := filepath.Base(root)
	if sensitiveNames[name] {
		return true
	}
	return sensitiveExts[strings.ToLower(filepath.Ext(name))]
}
