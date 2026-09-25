package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestValidateBaseRejectsCredentialURL(t *testing.T) {
	c := &Client{BaseURL: "https://user:secret@example.com"}
	if err := validateBase(c.BaseURL); err == nil {
		t.Fatal("credential URL must fail")
	}
	if err := validateBase("https://cp.stowline.local"); err != nil {
		t.Fatal(err)
	}
}

func TestForbiddenKindRejected(t *testing.T) {
	if _, ok := allowedKinds["EXEC"]; ok {
		t.Fatal("EXEC must not be allowed")
	}
	if _, ok := allowedKinds["RUN_BACKUP"]; !ok {
		t.Fatal("RUN_BACKUP")
	}
	if _, ok := allowedKinds["QUALIFICATION_BACKUP"]; ok {
		t.Fatal("qualification must not be a control-plane kind")
	}
	if _, ok := allowedKinds["RUN_QUALIFICATION"]; ok {
		t.Fatal("qualification must not be a control-plane kind")
	}
	if _, ok := allowedKinds["BROWSE_LOCAL_DIR"]; !ok {
		t.Fatal("BROWSE_LOCAL_DIR")
	}
	if _, ok := allowedKinds["APPLY_SELECTION"]; !ok {
		t.Fatal("APPLY_SELECTION")
	}
}

func TestAbortEnrollmentPostsBearerAndOmitsBodySecrets(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schema_version":1,"lifecycle":"QUARANTINED"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	if err := c.AbortEnrollment(context.Background(), domain.CanarySecret); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/enrollments/abort" {
		t.Fatalf("path %s", gotPath)
	}
	if gotAuth != "Bearer "+domain.CanarySecret {
		t.Fatal("abort must use the control bearer")
	}
	if strings.Contains(gotBody, domain.CanarySecret) {
		t.Fatal("abort JSON body must not echo the credential")
	}
	if !strings.Contains(gotBody, "local_persistence_failed") {
		t.Fatalf("abort reason: %s", gotBody)
	}
}

func TestRenewLeaseSendsNumericProgressOnly(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"status":"GRANTED","lease_id":"lease-1"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	progress := &domain.BackupProgress{PercentDone: 42.5, FilesDone: 4, TotalFiles: 10, BytesDone: 425, TotalBytes: 1000}
	if _, err := c.RenewLease(context.Background(), "lease-1", progress); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/wan/leases/lease-1/renew" {
		t.Fatalf("path %s", gotPath)
	}
	for _, want := range []string{`"percent_done":42.5`, `"files_done":4`, `"total_bytes":1000`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body %s missing %s", gotBody, want)
		}
	}
	if strings.Contains(gotBody, "control-secret") {
		t.Fatalf("credential leaked into body: %s", gotBody)
	}
}

func TestDownloadAgentBinaryReturnsExactBytes(t *testing.T) {
	payload := []byte("pretend-agent-binary-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/binary" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	got, err := c.DownloadAgentBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestDownloadAgentBinaryPropagatesAServerRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("agent build not qualified for distribution"))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	if _, err := c.DownloadAgentBinary(context.Background()); err == nil {
		t.Fatal("expected the 503 to surface as an error")
	}
}

func TestPutEscrowSendsKindAndSecretToTheRightPath(t *testing.T) {
	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	if err := c.PutEscrow(context.Background(), "restic-password", "s3cr3t-repo-pw"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/escrow" {
		t.Fatalf("path = %s", gotPath)
	}
	if !strings.Contains(gotBody, `"kind":"restic-password"`) || !strings.Contains(gotBody, `"secret":"s3cr3t-repo-pw"`) {
		t.Fatalf("body = %s", gotBody)
	}
	if strings.Contains(gotBody, "control-secret") {
		t.Fatalf("credential leaked into body: %s", gotBody)
	}
}

func TestReportSelfServiceRestoreSendsSnapshotSelectionsAndDestination(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	if err := c.ReportSelfServiceRestore(context.Background(), strings.Repeat("a", 64), []string{"/C/Belgeler/rapor.docx"}, `C:\Stowline-Recovery\r1\rapor.docx`); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/self-service-restores" {
		t.Fatalf("path = %s", gotPath)
	}
	if !strings.Contains(gotBody, "rapor.docx") || !strings.Contains(gotBody, "Stowline-Recovery") {
		t.Fatalf("body = %s", gotBody)
	}
	if strings.Contains(gotBody, "control-secret") {
		t.Fatalf("credential leaked into body: %s", gotBody)
	}
}

func TestCatalogClientMethodsHitTheRightPathsAndBodies(t *testing.T) {
	var gotPaths []string
	var gotBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, string(b))
		switch {
		case strings.HasSuffix(r.URL.Path, "/finalize"):
			_, _ = w.Write([]byte(`{"catalog_id":"cat-1","state":"READY","ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/fail"):
			_, _ = w.Write([]byte(`{"state":"FAILED"}`))
		case strings.HasSuffix(r.URL.Path, "/entries"):
			_, _ = w.Write([]byte(`{"state":"UPLOADING","received":1}`))
		default:
			_, _ = w.Write([]byte(`{"catalog_id":"cat-1","state":"PENDING"}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}

	catalogID, state, err := c.CreateCatalog(context.Background(), strings.Repeat("a", 64), map[string]any{"declared_file_count": 1, "declared_directory_count": 0, "declared_logical_bytes": 10, "declared_checksum": "x"})
	if err != nil || catalogID != "cat-1" || state != "PENDING" {
		t.Fatalf("create: %v %s %s", err, catalogID, state)
	}
	if err := c.UploadCatalogEntries(context.Background(), catalogID, []map[string]any{{"path": "/C/f.txt", "name": "f.txt", "type": "file"}}); err != nil {
		t.Fatalf("entries: %v", err)
	}
	ok, err := c.FinalizeCatalog(context.Background(), catalogID, map[string]any{"file_count": 1, "directory_count": 0, "logical_bytes": 10, "checksum": "x"})
	if err != nil || !ok {
		t.Fatalf("finalize: %v %v", err, ok)
	}
	if err := c.FailCatalog(context.Background(), catalogID, "AUTH"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	wantPaths := []string{"/api/v1/agent/snapshot-catalogs", "/api/v1/agent/snapshot-catalogs/cat-1/entries", "/api/v1/agent/snapshot-catalogs/cat-1/finalize", "/api/v1/agent/snapshot-catalogs/cat-1/fail"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("paths: %v", gotPaths)
	}
	for i, want := range wantPaths {
		if gotPaths[i] != want {
			t.Fatalf("path %d: got %s want %s", i, gotPaths[i], want)
		}
	}
	for _, b := range gotBodies {
		if strings.Contains(b, "control-secret") {
			t.Fatalf("credential leaked into body: %s", b)
		}
	}
}

func TestHeartbeatSendsTheRunningBinarysSHAWhenKnown(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"schema_version":1}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "tok"}
	sha := strings.Repeat("ab", 32)
	if _, err := c.Heartbeat(context.Background(), 1, "v", "none", "rev", sha); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"agent_sha256":"`+sha+`"`) {
		t.Fatalf("body = %s", gotBody)
	}
	if _, err := c.Heartbeat(context.Background(), 2, "v", "none", "rev", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "agent_sha256") {
		t.Fatalf("an unknown SHA must be omitted, not sent empty: %s", gotBody)
	}
}

func TestSyncSourceRootsTagsTheReportAsDeviceSync(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	if err := c.SyncSourceRoots(context.Background(), []string{"C:/Users/ali/Belgeler"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/self-service-selection" {
		t.Fatalf("path = %s", gotPath)
	}
	if !strings.Contains(gotBody, `"source":"device_sync"`) || !strings.Contains(gotBody, "Belgeler") {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestSelfServiceBackupAndUserMessageHitTheirEndpoints(t *testing.T) {
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"queued":true}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "control-secret"}
	if err := c.RequestSelfServiceBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.SendUserMessage(context.Background(), "Excel dosyamı bulamıyorum"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/api/v1/agent/self-service-backup" || paths[1] != "/api/v1/agent/user-messages" {
		t.Fatalf("paths = %v", paths)
	}
	if !strings.Contains(bodies[1], "Excel") {
		t.Fatalf("message body = %s", bodies[1])
	}
}
