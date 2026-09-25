package identity

import "testing"

func TestFormatProcessIdentity(t *testing.T) {
	if got := Format("S-1-5-18", true, true); got != "LocalSystem (S-1-5-18)" {
		t.Fatalf("got %q", got)
	}
	if got := Format("S-1-5-21-1-2-3-1001", false, true); got != "Interactive administrator (S-1-5-21-1-2-3-1001)" {
		t.Fatalf("got %q", got)
	}
	if got := Format("S-1-5-21-1-2-3-1001", false, false); got != "Interactive user (S-1-5-21-1-2-3-1001)" {
		t.Fatalf("got %q", got)
	}
	if Format("S-1-5-21-1-2-3-1001", false, true) == "LocalSystem (S-1-5-18)" {
		t.Fatal("must not infer LocalSystem from elevation")
	}
}
