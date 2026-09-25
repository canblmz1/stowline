package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestLocalUIStatusReportsIdleWhenNoBackupIsRunning(t *testing.T) {
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["backing_up"] != false {
		t.Fatalf("expected backing_up=false, got %+v", body)
	}
}

func TestLocalUIStatusReportsLiveProgressWhileBackingUp(t *testing.T) {
	p := &liveBackupProgress{}
	p.update(domain.BackupProgress{PercentDone: 42, FilesDone: 4, TotalFiles: 10})
	s := &localUIServer{Caller: testCaller, Progress: p}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/status", nil))
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["backing_up"] != true || body["percent_done"] != float64(42) {
		t.Fatalf("expected live progress reflected, got %+v", body)
	}
}

func TestLocalUIGetFolders(t *testing.T) {
	s := &localUIServer{Caller: testCaller,
		Progress:       &liveBackupProgress{},
		CurrentFolders: func() ([]string, error) { return []string{`C:\Users\ahmet\Belgeler`}, nil },
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/folders", nil))
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	roots := body["source_roots"].([]any)
	if len(roots) != 1 || roots[0] != `C:\Users\ahmet\Belgeler` {
		t.Fatalf("got %+v", body)
	}
}

func TestLocalUIPostFoldersAppliesOrdinaryFoldersDirectly(t *testing.T) {
	var applied []string
	s := &localUIServer{Caller: testCaller,
		Progress:     &liveBackupProgress{},
		ApplyFolders: func(ctx context.Context, roots []string) error { applied = roots; return nil },
	}
	reqBody, _ := json.Marshal(map[string]any{"source_roots": []string{`C:\Users\ahmet\Belgeler`, `C:\Users\ahmet\Masaustu`}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/folders", bytes.NewReader(reqBody)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(applied) != 2 {
		t.Fatalf("expected ApplyFolders called with both roots, got %+v", applied)
	}
}

func TestLocalUIPostFoldersRequiresConfirmationForASensitivePath(t *testing.T) {
	applyCalled := false
	s := &localUIServer{Caller: testCaller,
		Progress:     &liveBackupProgress{},
		ApplyFolders: func(ctx context.Context, roots []string) error { applyCalled = true; return nil },
	}
	reqBody, _ := json.Marshal(map[string]any{"source_roots": []string{`C:\Users\ahmet\.env`}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/folders", bytes.NewReader(reqBody)))
	if rec.Code != 422 {
		t.Fatalf("expected 422 pending confirmation, got %d: %s", rec.Code, rec.Body)
	}
	if applyCalled {
		t.Fatal("must not apply a sensitive path without explicit confirmation")
	}

	// Retry with confirmation -- must now succeed.
	reqBody2, _ := json.Marshal(map[string]any{"source_roots": []string{`C:\Users\ahmet\.env`}, "confirmed_sensitive": []string{`C:\Users\ahmet\.env`}})
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, localReq("POST", "/api/folders", bytes.NewReader(reqBody2)))
	if rec2.Code != 200 {
		t.Fatalf("expected 200 after confirmation, got %d: %s", rec2.Code, rec2.Body)
	}
	if !applyCalled {
		t.Fatal("expected ApplyFolders called after explicit confirmation")
	}
}

func TestLocalUISnapshotsFiltersToSucceededOnesWithASnapshotID(t *testing.T) {
	s := &localUIServer{Caller: testCaller,
		Progress: &liveBackupProgress{},
		RecentAttempts: func(ctx context.Context, limit int) ([]ports.AttemptRecord, error) {
			return []ports.AttemptRecord{
				{SnapshotID: "aaa", Outcome: domain.PhaseSucceeded},
				{SnapshotID: "", Outcome: domain.PhaseSucceeded},
				{SnapshotID: "bbb", Outcome: domain.PhaseFailed},
			}, nil
		},
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/snapshots", nil))
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	snaps := body["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Fatalf("expected exactly one snapshot surfaced, got %+v", snaps)
	}
}

func TestLocalUIBrowseRequiresASnapshotID(t *testing.T) {
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, ListSnapshot: func(ctx context.Context, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error) {
		return nil, false, nil
	}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/browse", nil))
	if rec.Code != 400 {
		t.Fatalf("expected 400 without a snapshot id, got %d", rec.Code)
	}
}

func TestLocalUIBrowseReturnsEntries(t *testing.T) {
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, ListSnapshot: func(ctx context.Context, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error) {
		if snapshotID != "snap-1" || prefix != "/C" {
			t.Fatalf("unexpected args: %s %s", snapshotID, prefix)
		}
		return []ports.SnapshotEntry{{Name: "Belgeler", Type: "dir", Path: "/C/Belgeler"}}, false, nil
	}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/browse?snapshot=snap-1&prefix=%2FC", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func fakeRestores(run restoreFunc) *restoreJobs {
	return &restoreJobs{Run: run}
}

func TestLocalUIRestoreRequiresSnapshotAndSelections(t *testing.T) {
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, Restores: fakeRestores(func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
		return "", nil
	})}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/restore", bytes.NewReader([]byte(`{}`))))
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestLocalUIRestoreStartsInTheBackgroundAndIsPolledToDone(t *testing.T) {
	release := make(chan struct{})
	s := &localUIServer{Caller: testCaller,
		Progress: &liveBackupProgress{},
		Restores: fakeRestores(func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			progress(domain.RestoreProgress{PercentDone: 50, BytesDone: 5, TotalBytes: 10})
			<-release
			return "", nil
		}),
	}
	reqBody, _ := json.Marshal(map[string]any{"snapshot_id": "snap-1", "selections": []string{"/C/Belgeler"}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/restore", bytes.NewReader(reqBody)))
	if rec.Code != 202 {
		t.Fatalf("a restore must be accepted at once, got %d: %s", rec.Code, rec.Body)
	}
	var job map[string]any
	json.Unmarshal(rec.Body.Bytes(), &job)
	id := job["id"].(string)

	busy := httptest.NewRecorder()
	s.Handler().ServeHTTP(busy, localReq("POST", "/api/restore", bytes.NewReader(reqBody)))
	if busy.Code != 409 {
		t.Fatalf("a second restore while one runs must be refused, got %d", busy.Code)
	}

	status := httptest.NewRecorder()
	s.Handler().ServeHTTP(status, localReq("GET", "/api/status", nil))
	if !strings.Contains(status.Body.String(), `"restore"`) {
		t.Fatalf("status must carry the running restore: %s", status.Body)
	}
	close(release)
	waitState(t, s.Restores, id, restoreDone)
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, localReq("GET", "/api/restores/"+id, nil))
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"done"`) {
		t.Fatalf("poll = %d %s", got.Code, got.Body)
	}
}

func TestLocalUIRestoreCanBeCancelled(t *testing.T) {
	s := &localUIServer{Caller: testCaller,
		Progress: &liveBackupProgress{},
		Restores: fakeRestores(func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}),
	}
	job, _ := s.Restores.StartAs(testSID, "snap-1", []string{"/C/x"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/restores/"+job.ID+"/cancel", strings.NewReader(`{}`)))
	if rec.Code != 200 {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body)
	}
	waitState(t, s.Restores, job.ID, restoreCancelled)
}

func TestLocalUIBackupNowQueuesOnTheControlPlaneAndRefusesWhileRunning(t *testing.T) {
	calls := 0
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, RequestBackup: func(ctx context.Context) error { calls++; return nil }}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/backup-now", strings.NewReader(`{}`)))
	if rec.Code != 202 || calls != 1 {
		t.Fatalf("backup-now = %d calls=%d", rec.Code, calls)
	}
	s.Progress.update(domain.BackupProgress{PercentDone: 5})
	again := httptest.NewRecorder()
	s.Handler().ServeHTTP(again, localReq("POST", "/api/backup-now", strings.NewReader(`{}`)))
	if again.Code != 409 || calls != 1 {
		t.Fatalf("while running = %d calls=%d", again.Code, calls)
	}
}

func TestLocalUIHelpSendsTheMessageAndRejectsAnEmptyOne(t *testing.T) {
	var sent string
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, SendMessage: func(ctx context.Context, m string) error { sent = m; return nil }}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/help", strings.NewReader(`{"message":"  Excel dosyam yok  "}`)))
	if rec.Code != 200 || sent != "Excel dosyam yok" {
		t.Fatalf("help = %d sent=%q", rec.Code, sent)
	}
	empty := httptest.NewRecorder()
	s.Handler().ServeHTTP(empty, localReq("POST", "/api/help", strings.NewReader(`{"message":"   "}`)))
	if empty.Code != 400 {
		t.Fatalf("empty message = %d", empty.Code)
	}
}

func TestLocalUISearchFindsFilesByNameAndCachesTheListing(t *testing.T) {
	listings := 0
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}, ListSnapshotFull: func(ctx context.Context, snapshotID string) ([]ports.SnapshotEntry, error) {
		listings++
		return []ports.SnapshotEntry{
			{Name: "Faturalar", Type: "dir", Path: "/C/Muhasebe/Faturalar"},
			{Name: "fatura-listesi.csv", Type: "file", Path: "/C/Muhasebe/fatura-listesi.csv"},
			{Name: "notlar.txt", Type: "file", Path: "/C/notlar.txt"},
		}, nil
	}}
	for _, q := range []string{"fatura", "FATURA"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, localReq("GET", "/api/search?snapshot=s1&q="+q, nil))
		var body struct {
			Results []ports.SnapshotEntry `json:"results"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		if len(body.Results) != 1 || body.Results[0].Name != "fatura-listesi.csv" {
			t.Fatalf("q=%s results = %+v", q, body.Results)
		}
	}
	if listings != 1 {
		t.Fatalf("the snapshot listing must be cached, listed %d times", listings)
	}
}

func TestLocalUIStatusNamesTheBackupPhase(t *testing.T) {
	p := &liveBackupProgress{}
	p.update(domain.BackupProgress{PercentDone: 100, BytesDone: 10, TotalBytes: 10})
	s := &localUIServer{Caller: testCaller, Progress: p}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/api/status", nil))
	if !strings.Contains(rec.Body.String(), `"phase":"finalizing"`) {
		t.Fatalf("status = %s", rec.Body)
	}
}

func TestLocalUIIndexServesTheEmbeddedPage(t *testing.T) {
	s := &localUIServer{Caller: testCaller, Progress: &liveBackupProgress{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("GET", "/", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status=%d content-type=%s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Stowline Backups")) {
		t.Fatal("expected the embedded page content")
	}
}

// localReq builds a request exactly as the page served from this machine
// would send it: loopback Host, and for POSTs the JSON content type plus the
// custom header only same-origin script can set.
func localReq(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:18080"
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(localUIHeader, "1")
	}
	return r
}

func guardedServer(applied *bool) *localUIServer {
	return &localUIServer{Caller: testCaller,
		Progress:     &liveBackupProgress{},
		ApplyFolders: func(ctx context.Context, roots []string) error { *applied = true; return nil },
		Restores: fakeRestores(func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
			*applied = true
			return "x", nil
		}),
	}
}

func TestLocalUIRejectsAForeignHostHeader(t *testing.T) {
	// DNS rebinding: attacker.example resolves to 127.0.0.1, so the browser
	// reaches this server but still sends the attacker's hostname as Host.
	applied := false
	s := guardedServer(&applied)
	r := localReq("GET", "/api/folders", nil)
	r.Host = "attacker.example:18080"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("expected 403 for a rebinding Host, got %d", rec.Code)
	}
}

func TestLocalUIRejectsAStateChangeWithoutTheLocalHeader(t *testing.T) {
	// A plain cross-site HTML form can POST a text/plain body but can never
	// set a custom header.
	applied := false
	s := guardedServer(&applied)
	r := localReq("POST", "/api/restore", strings.NewReader(`{"snapshot_id":"s","selections":["/C/x"]}`))
	r.Header.Del(localUIHeader)
	r.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 || applied {
		t.Fatalf("expected 403 and no restore, got %d applied=%v", rec.Code, applied)
	}
}

func TestLocalUIRejectsAStateChangeFromAForeignOrigin(t *testing.T) {
	applied := false
	s := guardedServer(&applied)
	r := localReq("POST", "/api/folders", strings.NewReader(`{"source_roots":["C:/Users/ahmet/Belgeler"]}`))
	r.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 || applied {
		t.Fatalf("expected 403 and no change, got %d applied=%v", rec.Code, applied)
	}
}

func TestLocalUIAcceptsTheSameOriginPage(t *testing.T) {
	applied := false
	s := guardedServer(&applied)
	r := localReq("POST", "/api/folders", strings.NewReader(`{"source_roots":["C:/Users/ahmet/Belgeler"]}`))
	r.Header.Set("Origin", "http://127.0.0.1:18080")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 200 || !applied {
		t.Fatalf("expected the local page's own request to succeed, got %d applied=%v", rec.Code, applied)
	}
}

func TestLocalUIPageEscapesNamesBeforeInsertingThemAsHTML(t *testing.T) {
	// File and folder names come straight from backups; every one that
	// reaches innerHTML must go through esc().
	page := string(localUIPage)
	if !strings.Contains(page, "const esc =") {
		t.Fatal("page lost its HTML-escaping helper")
	}
	for _, raw := range []string{"${e.name}", "${r}", "${c.label}", "${e.path}"} {
		if strings.Contains(page, raw) {
			t.Fatalf("page interpolates %s into HTML without esc()", raw)
		}
	}
}

func TestLocalUIFolderChangeIsReportedToTheControlPlane(t *testing.T) {
	reported := make(chan []string, 1)
	s := &localUIServer{Caller: testCaller,
		Progress:                   &liveBackupProgress{},
		ApplyFolders:               func(ctx context.Context, roots []string) error { return nil },
		ReportSelfServiceSelection: func(ctx context.Context, roots []string) error { reported <- roots; return nil },
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/folders", strings.NewReader(`{"source_roots":["C:/Users/ahmet/Belgeler"]}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := <-reported; len(got) != 1 || got[0] != "C:/Users/ahmet/Belgeler" {
		t.Fatalf("expected the new roots reported, got %v", got)
	}
}

func TestLocalUIFailedFolderChangeIsNeverReported(t *testing.T) {
	called := false
	s := &localUIServer{Caller: testCaller,
		Progress:                   &liveBackupProgress{},
		ApplyFolders:               func(ctx context.Context, roots []string) error { return errors.New("rejected") },
		ReportSelfServiceSelection: func(ctx context.Context, roots []string) error { called = true; return nil },
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, localReq("POST", "/api/folders", strings.NewReader(`{"source_roots":["relative"]}`)))
	if rec.Code != 422 || called {
		t.Fatalf("expected 422 and no report, got %d called=%v", rec.Code, called)
	}
}

const testSID = "S-1-5-21-1-1001"

// testCaller is "ahmet" on a PC that bob also uses.
func testCaller(r *http.Request) (localCaller, error) {
	return newLocalCaller(testSID, `C:\Users\ahmet`, `C:\Users`, []string{`C:\Users\ahmet`, `C:\Users\bob`, `C:\Windows\ServiceProfiles\LocalService`}), nil
}
