package qualification

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func validReq() Request {
	return Request{
		Schema:             RequestSchema,
		QualificationRunID: "qualrun-001",
		Operation:          OperationBackup,
		SyntheticCorpus:    true,
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"schema":"stowline.qualification.request.v1","qualification_run_id":"qualrun-001","operation":"BACKUP","synthetic_corpus":true,"source_root":"C:\\Windows"}`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("arbitrary source field must be rejected")
	}
	raw = []byte(`{"schema":"stowline.qualification.request.v1","qualification_run_id":"qualrun-001","operation":"BACKUP","synthetic_corpus":true,"repository":"s3:evil"}`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("arbitrary repository field must be rejected")
	}
	raw = []byte(`{"schema":"stowline.qualification.request.v1","qualification_run_id":"qualrun-001","operation":"BACKUP","synthetic_corpus":true,"args":["--verbose"]}`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("arbitrary args must be rejected")
	}
}

func TestParseRejectsArbitraryOperation(t *testing.T) {
	for _, op := range []string{"SHELL", "RESTORE", "RUN_BACKUP", "FORGET", "EXEC"} {
		req := validReq()
		req.Operation = op
		b, _ := json.Marshal(req)
		_, err := ParseRequest(b)
		if err == nil {
			t.Fatalf("operation %s must be rejected", op)
		}
		if !LooksLikeArbitraryCommand(op) && op != "RESTORE" && op != "RUN_BACKUP" {
			t.Fatalf("LooksLikeArbitraryCommand missed %s", op)
		}
	}
}

func TestParseRequiresSyntheticCorpus(t *testing.T) {
	req := validReq()
	req.SyntheticCorpus = false
	b, _ := json.Marshal(req)
	if _, err := ParseRequest(b); err == nil {
		t.Fatal("synthetic_corpus=false must be rejected")
	}
}

func TestBoundRequestCannotCarryCallerSourceOrRepo(t *testing.T) {
	src := []string{PilotSyntheticCorpus}
	repo := domain.RepositoryDescriptor{
		BackendKind:  domain.BackendLocal,
		Location:     `C:\Stowline\Repos\local`,
		Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
	}
	req := BoundBackupRequest("qualrun-001", src, repo, domain.VSSRequired, domain.SecretRef{Locator: "x"}, "pilot-device", "pilot-install", "pilot-local-1", `C:\Stowline\cache`, "pilot-device", 0, 0)
	if req.AttemptClass != domain.AttemptQualification {
		t.Fatal(req.AttemptClass)
	}
	if req.SlotKey != domain.QualificationSlotKey("qualrun-001") {
		t.Fatal(req.SlotKey)
	}
	if req.Repository.Location != repo.Location {
		t.Fatal("repository must be bound from config")
	}
	if len(req.Excludes) != 0 {
		t.Fatal("no extra excludes")
	}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledRequestRejectsQualificationSlot(t *testing.T) {
	req := domain.BackupRequest{
		Repository: domain.RepositoryDescriptor{
			BackendKind:  domain.BackendLocal,
			Location:     `C:\repo`,
			Capabilities: domain.DefaultCapabilities(domain.BackendLocal),
		},
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSRequired,
		SlotKey:     domain.QualificationSlotKey("qualrun-001"),
	}
	if err := req.Validate(); err == nil {
		t.Fatal("scheduled backup must not use qualification slot")
	}
}

func TestBindSourcesRejectsMismatch(t *testing.T) {
	_, err := BindSources([]string{`C:\Windows`}, []string{PilotSyntheticCorpus})
	if err == nil {
		t.Fatal("must refuse arbitrary configured source")
	}
}

func TestExpandWorkloadStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	p, err := ExpandWorkload(root, "qualrun-001", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(p), strings.ToLower(root)) {
		t.Fatalf("%s not under %s", p, root)
	}
}
