package main

import "testing"

func TestIsSensitivePathMatchesKnownCredentialNamesAndExtensions(t *testing.T) {
	sensitive := []string{
		`C:\Users\ahmet\.env`,
		`C:\Users\ahmet\secrets.json`,
		`C:\Users\ahmet\id_rsa`,
		`C:\Users\ahmet\key.pem`,
		`C:\Users\ahmet\cert.pfx`,
	}
	for _, p := range sensitive {
		if !isSensitivePath(p) {
			t.Fatalf("expected %s to be flagged sensitive", p)
		}
	}
}

func TestIsSensitivePathAllowsOrdinaryFolders(t *testing.T) {
	ordinary := []string{
		`C:\Users\ahmet\Documents`,
		`C:\Users\ahmet\Desktop`,
		`C:\Stowline\TestCorpus`,
	}
	for _, p := range ordinary {
		if isSensitivePath(p) {
			t.Fatalf("expected %s to NOT be flagged sensitive", p)
		}
	}
}
