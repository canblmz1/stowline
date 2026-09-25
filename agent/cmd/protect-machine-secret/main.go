// Command protect-machine-secret reads one secret from stdin and writes an
// Stowline machine-scope DPAPI envelope. It does not print the secret.
//
//	some-secret | go run ./agent/cmd/protect-machine-secret --name gateway-rest-password.machine.dpapi
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
)

func main() {
	name := flag.String("name", "", "envelope file name under --dir")
	dir := flag.String("dir", `C:\Stowline\secrets`, "secrets directory")
	flag.Parse()
	if *name == "" || strings.Contains(*name, "..") || strings.ContainsAny(*name, `/\`) {
		fmt.Fprintln(os.Stderr, "usage: protect-machine-secret --name <basename.machine.dpapi>")
		os.Exit(2)
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdin read failed")
		os.Exit(1)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		fmt.Fprintln(os.Stderr, "empty stdin")
		os.Exit(1)
	}
	p := secrets.NewService(*dir)
	if err := p.Protect(*name, raw); err != nil {
		fmt.Fprintln(os.Stderr, "protect failed")
		os.Exit(1)
	}
	for i := range raw {
		raw[i] = 0
	}
	fmt.Fprintln(os.Stdout, filepath.Join(*dir, *name))
}
