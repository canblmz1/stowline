package secrets

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestFileProviderReadsAndWipes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(p, []byte("  file-secret-value  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := FileProvider{}.Open(context.Background(), domain.SecretRef{Locator: p})
	if err != nil {
		t.Fatal(err)
	}
	if string(h.Bytes()) != "file-secret-value" {
		t.Fatalf("%q", h.Bytes())
	}
	_ = h.Close()
	if len(h.Bytes()) != 0 {
		t.Fatal("expected wipe")
	}
}

func TestFileProviderRejectsURL(t *testing.T) {
	_, err := FileProvider{}.Open(context.Background(), domain.SecretRef{Locator: "https://example.invalid/secret"})
	if err == nil {
		t.Fatal("expected reject")
	}
}

func TestStaticProvider(t *testing.T) {
	p := StaticProvider{Values: map[string]string{"k": "v"}}
	h, err := p.Open(context.Background(), domain.SecretRef{Locator: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !bytes.Equal(h.Bytes(), []byte("v")) {
		t.Fatal(h.Bytes())
	}
}
