package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestReviewUnimplementedBackendsFailClosed(t *testing.T) {
	for _, kind := range []domain.BackendKind{domain.BackendS3, domain.BackendB2} {
		d := domain.RepositoryDescriptor{BackendKind: kind, Location: "s3:example", Capabilities: domain.DefaultCapabilities(kind)}
		if _, err := (Connector{}).Bind(context.Background(), d); !errors.Is(err, domain.ErrCapabilityUnsupported) {
			t.Fatalf("%s: expected unsupported, got %v", kind, err)
		}
	}
}

func TestRESTGatewayBindLabHTTP(t *testing.T) {
	d := domain.RepositoryDescriptor{
		BackendKind:      domain.BackendRESTGateway,
		Location:         "rest:http://127.0.0.1:8081/restic/dev/gen",
		TransportProfile: "lab-insecure-http",
		Capabilities:     domain.DefaultCapabilities(domain.BackendRESTGateway),
		GenerationID:     "g",
		DeviceID:         "d",
	}
	b, err := (Connector{}).Bind(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if b.Location != d.Location {
		t.Fatal(b.Location)
	}
	d.TransportProfile = ""
	if _, err := (Connector{}).Bind(context.Background(), d); err == nil {
		t.Fatal("plain HTTP without lab profile must fail")
	}
	d.TransportProfile = "lab-insecure-http"
	d.Location = "rest:http://user:pass@127.0.0.1/restic/x"
	if _, err := (Connector{}).Bind(context.Background(), d); err == nil {
		t.Fatal("credential URL must fail")
	}
}
func TestReviewRcloneUsesAbsoluteProgramAndDependencyPin(t *testing.T) {
	pin := domain.BinaryPin{Path: `C:\Stowline\bin\rclone.exe`, SHA256: "test-pin"}
	c := Connector{RclonePin: pin}
	d := RcloneLocalDescriptor("rclone:local:repo", "", "g", "d")
	b, err := c.Bind(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	// Unquoted forward-slash absolute path: survives restic CSV + SplitShellStrings
	// and forces Go exec to the pinned file with no PATH/CWD search.
	if len(b.Options) != 1 || b.Options[0].Value != filepath.ToSlash(pin.Path) || len(b.BinaryPins) != 1 || b.BinaryPins[0] != pin {
		t.Fatalf("absolute executable and digest dependency not preserved: %+v", b.Options)
	}
	// A path with a space cannot be expressed safely -> fail closed.
	if _, err := (Connector{RclonePin: domain.BinaryPin{Path: filepath.Join(t.TempDir(), "space dir", "rclone.exe"), SHA256: "p"}}).
		Bind(context.Background(), d); err == nil {
		t.Fatal("rclone pin path with a space must be rejected")
	}
}
