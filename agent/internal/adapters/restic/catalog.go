package restic

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// CatalogChecksum must produce the exact same value as the server's
// Python catalog_checksum() for the same set of path hashes -- both sides
// sort first specifically so upload batch order never matters.
func CatalogChecksum(pathHashes []string) string {
	sorted := append([]string(nil), pathHashes...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// PathHash must produce the exact same value as the server's Python
// catalog_path_hash() for the same path string.
func PathHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}
