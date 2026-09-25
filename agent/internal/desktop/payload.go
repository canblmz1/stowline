package desktop

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const payloadMarker = ".stowline-payload-sha256"

// ExtractPayload unpacks the installer package embedded in the single-file
// Stowline Setup.exe into dest. When dest already holds this exact payload
// (same SHA-256 marker) it is left as is, so a second run starts instantly.
// Entries that would land outside dest are refused.
func ExtractPayload(data []byte, dest string) error {
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	if got, err := os.ReadFile(filepath.Join(dest, payloadMarker)); err == nil && strings.TrimSpace(string(got)) == want {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("embedded package unreadable: %w", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		target := filepath.Join(root, name)
		if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			return fmt.Errorf("embedded package has an unsafe path: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractOne(f, target); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(root, payloadMarker), []byte(want+"\n"), 0o644)
}

func extractOne(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
