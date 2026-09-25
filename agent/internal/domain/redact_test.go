package domain

import "testing"

func TestRedactRemovesCanaryAndPassword(t *testing.T) {
	s := "RESTIC_PASSWORD=" + CanarySecret + " Authorization: Bearer abc.def"
	out := Redact(s)
	if ContainsSecret(out) {
		t.Fatalf("still contains secret: %s", out)
	}
	if !ContainsSecret(s) {
		t.Fatal("detector missed canary")
	}
}

func TestRedactArgs(t *testing.T) {
	args := []string{"restic", "backup", "--password=secret", "--repo", "C:\\r"}
	out := RedactArgs(args)
	if ContainsSecret(out[2], "secret") && out[2] == "--password=secret" {
		t.Fatal(out)
	}
}

func TestLooksLikeCredentialURL(t *testing.T) {
	d := RepositoryDescriptor{
		BackendKind:  BackendRESTGateway,
		Location:     "rest:https://user:pass@gateway/repo",
		Capabilities: DefaultCapabilities(BackendRESTGateway),
	}
	if err := d.Validate(); err == nil {
		t.Fatal("credential URL must fail")
	}
	d.Location = "rest:https://gateway.example/repo"
	d.TLS.InsecureSkipVerify = true
	if err := d.Validate(); err == nil {
		t.Fatal("skip verify must fail")
	}
}
