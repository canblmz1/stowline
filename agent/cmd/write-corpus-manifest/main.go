// Command write-corpus-manifest regenerates TestCorpus/manifest.sha256.json
// from the live tree. Qualification must run this AFTER corpus mutation and
// BEFORE restic backup so the snapshot-embedded reference matches captured bytes.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/pilot/corpus"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--root" || strings.TrimSpace(os.Args[2]) == "" {
		fmt.Fprintln(os.Stderr, "usage: write-corpus-manifest --root <dir>")
		os.Exit(2)
	}
	if _, err := corpus.WriteManifest(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
