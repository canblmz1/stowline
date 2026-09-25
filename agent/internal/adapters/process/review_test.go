package process

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewArgvHelper(t *testing.T) {
	if os.Getenv("STOWLINE_REVIEW_HELPER") != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			json.NewEncoder(os.Stdout).Encode(os.Args[i+1:])
			break
		}
	}
	fmt.Fprint(os.Stderr, os.Getenv("RESTIC_PASSWORD"))
	os.Exit(0)
}
func TestReviewWindowsArgumentsAndSecretOutput(t *testing.T) {
	exe, _ := os.Executable()
	sum, err := hashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"space value", `a"b`, `C:\trailing\`, "-dash", "Branch_日本語", "key=value", "C:colon", strings.Repeat("long", 1000)}
	secret := "CANARY_RESTIC_PASSWORD_" + "review-unique"
	res, err := NewRunner().Run(context.Background(), ports.Spec{Executable: exe, ExpectedSHA256: sum, Args: append([]string{"-test.run=^TestReviewArgvHelper$", "--"}, args...), Env: []ports.EnvVar{{Name: "STOWLINE_REVIEW_HELPER", Value: "1"}, {Name: "RESTIC_PASSWORD", Value: secret, Secret: true}}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(res.Stdout, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatal("Windows argument roundtrip mismatch")
	}
	if strings.Contains(string(res.Stderr), secret) {
		t.Fatal("known secret escaped process output boundary")
	}
	if string(res.Stderr) != "[REDACTED]" {
		t.Fatal("helper did not exercise stderr redaction")
	}
}
func TestReviewDependencyTamperBlocksLaunch(t *testing.T) {
	exe, _ := os.Executable()
	sum, err := hashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunner().Run(context.Background(), ports.Spec{Executable: exe, ExpectedSHA256: sum, Dependencies: []domain.BinaryPin{{Path: exe, SHA256: strings.Repeat("0", 64)}}})
	if err == nil {
		t.Fatal("mismatched dependency digest accepted")
	}
}
