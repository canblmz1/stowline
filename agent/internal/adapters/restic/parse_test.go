package restic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestParseBackupSummaryGolden(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "contracts", "fixtures", "restic", "v0.19.1")
	b, err := os.ReadFile(filepath.Join(root, "backup_summary.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := parseBackupSummary(b)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.MessageSeen || sum.SnapshotID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("%+v", sum)
	}
}

func TestParseBackupSummaryTextSnapshotSaved(t *testing.T) {
	raw := []byte("no parent snapshot found, will read all files\n\nFiles:           1 new,     0 changed,     0 unmodified\nprocessed 1 files, 26 B in 0:00\nsnapshot 5a8c23f8 saved\n")
	sum, err := parseBackupSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.MessageSeen || sum.SnapshotID != "5a8c23f8" {
		t.Fatalf("%+v", sum)
	}
}

func TestParseUnknownJSONFieldDoesNotCrash(t *testing.T) {
	raw := []byte(`{"message_type":"summary","snapshot_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","brand_new_field":true,"files_new":1}
{"message_type":"future_unknown","x":1}
`)
	sum, err := parseBackupSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if sum.SnapshotID == "" || !sum.MessageSeen {
		t.Fatalf("%+v", sum)
	}
}

func TestParseSnapshotsArray(t *testing.T) {
	raw := []byte(`[{"id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","time":"2026-09-09T00:00:00Z","hostname":"dev","paths":["C:\\x"],"tags":["stowline"]}]`)
	snaps, err := parseSnapshots(raw)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("%v %+v", err, snaps)
	}
}

func TestVSSParserSuccessAndFailure(t *testing.T) {
	ok := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\n"
	ev := parseVSSEvidence(ok, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1 compiled with go1.26.4 on windows/amd64")
	if ev.Class() != domain.ConsistencyVerified {
		t.Fatalf("want verified: %+v", ev)
	}
	fail := "creating VSS snapshot for [c:\\]\nfailed to create snapshot for [c:\\]: Access is denied.\n"
	ev2 := parseVSSEvidence(fail, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	if ev2.Class() != domain.ConsistencyNotMet {
		t.Fatalf("want not met: %+v", ev2)
	}
	if ev2.PositiveAllRequired {
		t.Fatal("must not treat fallback as success")
	}
}

func TestVSSPrivilegeDenialIsNotSuccess(t *testing.T) {
	line := "VSS error: The caller does not have sufficient backup privileges or is not an administrator: E_ACCESSDENIED (0x455f41434345535344454e494544)"
	ev := parseVSSEvidence(line, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1 compiled with go1.26.4 on windows/amd64")
	if ev.Class() != domain.ConsistencyNotMet {
		t.Fatalf("%+v", ev)
	}
	if len(ev.RawMatchedLines) == 0 {
		t.Fatal("expected privilege line to match")
	}
}

func TestVSSDisabledIsLiveRead(t *testing.T) {
	ev := parseVSSEvidence("", domain.VSSDisabled, nil, "restic 0.19.1")
	if ev.Class() != domain.LiveReadAllowed {
		t.Fatal(ev.Class())
	}
}

func TestVSSSilenceIsNotPositive(t *testing.T) {
	ev := parseVSSEvidence("", domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	if ev.PositiveAllRequired || ev.Class() == domain.ConsistencyVerified {
		t.Fatalf("silence must not be positive: %+v", ev)
	}
}

func TestVSSPartialVolumeIsNotPositive(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\ncreating VSS snapshot for [d:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`, `d:\`}, "restic 0.19.1")
	if ev.PositiveAllRequired {
		t.Fatal("missing volume success must not be positive")
	}
}

func TestVSSFailureAfterSuccessIsNotPositive(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\nfailed to create snapshot for [c:\\]: later failure\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	if ev.PositiveAllRequired {
		t.Fatal("failure after success must not stay positive")
	}
}

func TestVSSMultipleVolumesAllSucceed(t *testing.T) {
	text := "creating VSS snapshot for [c:\\]\nsuccessfully created snapshot for [c:\\]\ncreating VSS snapshot for [d:\\]\nsuccessfully created snapshot for [d:\\]\n"
	ev := parseVSSEvidence(text, domain.VSSRequired, []string{`c:\`, `d:\`}, "restic 0.19.1")
	if !ev.PositiveAllRequired {
		t.Fatalf("all volumes succeeded: %+v", ev)
	}
}

func TestVSSMalformedTextIsNotPositive(t *testing.T) {
	ev := parseVSSEvidence("VSS did a thing\nsnapshot maybe\n", domain.VSSRequired, []string{`c:\`}, "restic 0.19.1")
	if ev.PositiveAllRequired {
		t.Fatal("malformed text must not be treated as success")
	}
}

// Real schema, confirmed live against the pinned restic 0.19.1 binary: `ls`
// with no path argument is fully recursive from the filesystem root, not
// "just the top-level backed-up roots" -- it emits every ancestor directory
// node down to the actual files. parseLsEntries is what narrows this to one
// level.
const lsRootFixture = `{"time":"2026-09-18T10:54:07+03:00","tree":"688545f...","paths":["/some/source"],"hostname":"can","id":"34a4bee1...","short_id":"34a4bee1","message_type":"snapshot","struct_type":"snapshot"}
{"name":"C","type":"dir","path":"/C","uid":0,"gid":0,"mode":2147484159,"permissions":"drwxrwxrwx","mtime":"2026-09-17T17:50:39+03:00","message_type":"node","struct_type":"node"}
{"name":"Users","type":"dir","path":"/C/Users","uid":0,"gid":0,"mode":2147484013,"mtime":"2026-08-29T10:40:14+03:00","message_type":"node","struct_type":"node"}
{"name":"alice","type":"dir","path":"/C/Users/alice","uid":0,"gid":0,"mtime":"2026-09-18T10:33:04+03:00","message_type":"node","struct_type":"node"}
`

func TestParseLsEntriesRootLevelExcludesDeeperAncestors(t *testing.T) {
	entries := parseLsEntries([]byte(lsRootFixture), "")
	if len(entries) != 1 {
		t.Fatalf("root listing must contain only the single top-level root, got %+v", entries)
	}
	if entries[0].Path != "/C" || entries[0].Type != "dir" {
		t.Fatalf("%+v", entries[0])
	}
}

const lsScopedFixture = `{"time":"2026-09-18T10:54:07+03:00","tree":"688545f...","paths":["/some/source"],"hostname":"can","id":"34a4bee1...","short_id":"34a4bee1","message_type":"snapshot","struct_type":"snapshot"}
{"name":"Belgeler","type":"dir","path":"/C/Belgeler","uid":0,"gid":0,"mtime":"2026-09-17T17:50:39+03:00","message_type":"node","struct_type":"node"}
{"name":"ek-teklif-v2.txt","type":"file","path":"/C/Belgeler/ek-teklif-v2.txt","uid":0,"gid":0,"size":1234,"mode":438,"permissions":"-rw-rw-rw-","mtime":"2026-09-18T09:00:00+03:00","message_type":"node","struct_type":"node"}
{"name":"Arsiv","type":"dir","path":"/C/Belgeler/Arsiv","uid":0,"gid":0,"mtime":"2026-09-10T00:00:00+03:00","message_type":"node","struct_type":"node"}
{"name":"old.txt","type":"file","path":"/C/Belgeler/Arsiv/old.txt","uid":0,"gid":0,"size":99,"mtime":"2026-01-01T00:00:00+03:00","message_type":"node","struct_type":"node"}
`

func TestParseLsEntriesScopedExcludesSelfAndGrandchildren(t *testing.T) {
	entries := parseLsEntries([]byte(lsScopedFixture), "/C/Belgeler")
	if len(entries) != 2 {
		t.Fatalf("must contain exactly the two direct children, got %+v", entries)
	}
	byPath := map[string]ports.SnapshotEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	file, ok := byPath["/C/Belgeler/ek-teklif-v2.txt"]
	if !ok || file.Type != "file" || file.Size != 1234 || file.Name != "ek-teklif-v2.txt" {
		t.Fatalf("%+v", byPath)
	}
	if _, grandchildLeaked := byPath["/C/Belgeler/Arsiv/old.txt"]; grandchildLeaked {
		t.Fatal("a grandchild (two levels down) must not appear in a one-level listing")
	}
	if _, selfLeaked := byPath["/C/Belgeler"]; selfLeaked {
		t.Fatal("the requested directory's own node must not appear as its own child")
	}
}

func TestParseLsEntriesNeverExposesInternalFields(t *testing.T) {
	entries := parseLsEntries([]byte(lsScopedFixture), "/C/Belgeler")
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"uid", "gid", "mode", "permissions", "struct_type", "message_type"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("serialized entries must never carry restic-internal field %q: %s", forbidden, raw)
		}
	}
}

func TestParseLsEntriesTrimsTrailingSlashOnPrefix(t *testing.T) {
	a := parseLsEntries([]byte(lsScopedFixture), "/C/Belgeler")
	b := parseLsEntries([]byte(lsScopedFixture), "/C/Belgeler/")
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("a trailing slash on the prefix must not change the result: %d vs %d", len(a), len(b))
	}
}

func TestParseLsEntriesFullReturnsEveryDepth(t *testing.T) {
	entries := parseLsEntriesFull([]byte(lsScopedFixture))
	if len(entries) != 4 {
		t.Fatalf("expected all 4 nodes at any depth, got %d: %+v", len(entries), entries)
	}
	byPath := map[string]ports.SnapshotEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if _, ok := byPath["/C/Belgeler/Arsiv/old.txt"]; !ok {
		t.Fatalf("a grandchild must be present in the full-tree listing: %+v", byPath)
	}
	deep := byPath["/C/Belgeler/Arsiv/old.txt"]
	if deep.Size != 99 || deep.Type != "file" {
		t.Fatalf("deep node not preserved correctly: %+v", deep)
	}
}

func TestCatalogChecksumIsOrderIndependent(t *testing.T) {
	a := CatalogChecksum([]string{"hash1", "hash2"})
	b := CatalogChecksum([]string{"hash2", "hash1"})
	if a != b {
		t.Fatalf("checksum must not depend on input order: %s vs %s", a, b)
	}
	if a == CatalogChecksum([]string{"hash1"}) {
		t.Fatal("a different entry set must produce a different checksum")
	}
}

func TestPathHashIsStable(t *testing.T) {
	if PathHash("/C/f.txt") != PathHash("/C/f.txt") {
		t.Fatal("path hash must be deterministic")
	}
	if PathHash("/C/f.txt") == PathHash("/C/g.txt") {
		t.Fatal("different paths must hash differently")
	}
	if len(PathHash("/C/f.txt")) != 64 {
		t.Fatalf("expected a 64-char hex sha256, got %d chars", len(PathHash("/C/f.txt")))
	}
}

func TestExitErrorJSONRedacted(t *testing.T) {
	raw := []byte(`{"message_type":"exit_error","code":12,"message":"wrong password"}`)
	ee := parseExitError(raw)
	if ee == nil || ee.Code != 12 {
		t.Fatalf("%+v", ee)
	}
}
