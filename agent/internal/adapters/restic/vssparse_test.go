package restic

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func vssFixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "contracts", "fixtures", "restic", "v0.19.1")
}

func assertVSSClass(t *testing.T, ev domain.VSSEvidence, want domain.ConsistencyClass) {
	t.Helper()
	if ev.Class() != want {
		t.Fatalf("class=%s want=%s positive=%v vols=%+v lines=%v notes=%v", ev.Class(), want, ev.PositiveAllRequired, ev.Volumes, ev.RawMatchedLines, ev.ParserNotes)
	}
}

func TestVSSRealRestic0191PositiveFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(vssFixtureDir(t), "vss_positive.restic-0.19.1.windows-c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `creating VSS snapshot for [c:\]`) || !strings.Contains(text, `successfully created snapshot for [c:\]`) {
		t.Fatalf("golden fixture missing real restic 0.19.1 VSS lines: %q", text)
	}
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`C:`}, "restic 0.19.1 compiled with go1.26.4 on windows/amd64")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
	if !ev.PositiveAllRequired {
		t.Fatal("expected positive all required")
	}
	foundC := false
	for _, v := range ev.Volumes {
		if normalizeVolumeKey(v.Volume) == "c:" && v.SuccessSeen && !v.FailureSeen {
			foundC = true
		}
	}
	if !foundC {
		t.Fatalf("vss_volumes must include C: %+v", ev.Volumes)
	}
	joined := strings.Join(ev.RawMatchedLines, "\n")
	if !strings.Contains(strings.ToLower(joined), "successfully created snapshot for") {
		t.Fatalf("matched_lines missing success evidence: %v", ev.RawMatchedLines)
	}
}

func TestVSSNoLinesIsNotVerified(t *testing.T) {
	ev := parseVSSEvidence("", domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
	if ev.PositiveAllRequired {
		t.Fatal("silence must not be positive")
	}
}

func TestVSSCreatingWithoutSuccessIsNotVerified(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}

func TestVSSOneVolumeSucceedsAnotherAbsent(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`, `d:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}

func TestVSSSuccessThenFailureSameVolume(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nfailed to create snapshot for [c:\\]: later failure\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}

func TestVSSDriveLetterCaseNormalization(t *testing.T) {
	text := "creating VSS snapshot for [C:\\]\nsuccessfully created snapshot for [C:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
	ev2 := parseVSSEvidence(
		"creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n",
		domain.VSSRequired,
		[]string{`C:`},
		"restic 0.19.1",
	)
	assertVSSClass(t, ev2, domain.ConsistencyVerified)
}

func TestVSSStdoutOnlyEvidence(t *testing.T) {
	stdout := []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n")
	ev := parseVSSEvidenceFromProcess(stdout, nil, false, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
}

func TestVSSStderrOnlyEvidence(t *testing.T) {
	stderr := []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n")
	ev := parseVSSEvidenceFromProcess(nil, stderr, false, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
}

func TestVSSSplitEvidenceAcrossStdoutAndStderr(t *testing.T) {
	stdout := []byte("creating VSS snapshot for [c:\\]\n")
	stderr := []byte("successfully created snapshot for [c:\\]\n")
	ev := parseVSSEvidenceFromProcess(stdout, stderr, false, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
	ev2 := parseVSSEvidenceFromProcess(stderr, stdout, false, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev2, domain.ConsistencyVerified)
}

func TestVSSTruncatedOutputNotVerified(t *testing.T) {
	stdout := []byte("creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n")
	ev := parseVSSEvidenceFromProcess(stdout, nil, true, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
	if ev.PositiveAllRequired {
		t.Fatal("truncated capture must not be VERIFIED")
	}
}

func TestVSSLinesSurviveSecretRedaction(t *testing.T) {
	secret := "pw-unique-vss-redact-test-9f3a"
	raw := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nRESTIC_PASSWORD=" + secret + "\n"
	redacted := bytes.ReplaceAll([]byte(raw), []byte(secret), []byte("[REDACTED]"))
	if bytes.Contains(redacted, []byte(secret)) {
		t.Fatal("secret remained after redaction")
	}
	ev := parseVSSEvidence(string(redacted), domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyVerified)
	joined := strings.Join(ev.RawMatchedLines, "\n")
	if !strings.Contains(joined, "successfully created snapshot for") {
		t.Fatalf("redaction removed VSS success line: %v", ev.RawMatchedLines)
	}
	if strings.Contains(joined, secret) {
		t.Fatal("secret leaked into matched VSS lines")
	}
}

func TestVSSNeverInferredFromEmptySuccess(t *testing.T) {
	ev := parseVSSEvidence("backup finished\n", domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}

func TestVSSFailureThenSuccessSameVolumeIsNotVerified(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nfailed to create snapshot for [c:\\]: first\nsuccessfully created snapshot for [c:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}

func TestVSSUnrelatedLocalizedTextIsNotEvidence(t *testing.T) {
	text := "Yedekleme tamamlandı\nbackup finished successfully\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	assertVSSClass(t, ev, domain.ConsistencyNotMet)
}
