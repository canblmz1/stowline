package process

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestRESTDeviceIDInRepoURLIsNotSecretArg(t *testing.T) {
	device := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	repo := "rest:http://127.0.0.1:8081/restic/" + device + "/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	spec := ports.Spec{
		Executable: `C:\Stowline\bin\restic.exe`,
		Args:       []string{"init", "--repo", repo},
		Env: []ports.EnvVar{
			{Name: "RESTIC_REST_USERNAME", Value: device, Secret: false},
			{Name: "RESTIC_REST_PASSWORD", Value: "gateway-secret-value", Secret: true},
			{Name: "RESTIC_PASSWORD", Value: "repo-secret-value", Secret: true},
		},
	}
	if err := rejectSecretArgs(spec, secretValues(spec.Env)); err != nil {
		t.Fatalf("device id is an identity in the REST path, not a secret: %v", err)
	}
	spec.Env[0].Secret = true
	if err := rejectSecretArgs(spec, secretValues(spec.Env)); err == nil {
		t.Fatal("marking the device id Secret must still reject it when it appears in argv")
	}
}
