package restic

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestClassifyExit(t *testing.T) {
	ok := classifyExit(0)
	if !ok.OK || !ok.Known {
		t.Fatalf("%+v", ok)
	}
	p := classifyExit(3)
	if !p.Partial || p.OK {
		t.Fatalf("exit 3 must be partial, not success: %+v", p)
	}
	u := classifyExit(99)
	if u.Known || u.OK {
		t.Fatalf("unknown must fail: %+v", u)
	}
	if classifyExit(12).Class != domain.ErrorAuth {
		t.Fatal("wrong password must be AUTH")
	}
	if classifyExit(12).OK {
		t.Fatal("wrong password must not be success")
	}
}
