package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestSnapshotPathKeyAcceptsWindowsAndSnapshotForms(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\Ahmet\`:     "/c/users/ahmet",
		"/C/Users/ahmet/Docs": "/c/users/ahmet/docs",
		"C:/Users/x":          "/c/users/x",
		"":                    "",
	} {
		if got := snapshotPathKey(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestWhoSeesWhat(t *testing.T) {
	c, _ := testCaller(nil)
	visible := []string{"/C/Users/ahmet/Belgeler/a.xlsx", "/C/Users/Public/Documents", "/C/Muhasebe", "/C/Users", "/C", `C:\Users\AHMET\Desktop`}
	hidden := []string{"/C/Users/bob", "/C/Users/bob/Documents/maas.xlsx", "/C/Users/eski-calisan/Belgeler", "/C/Users/ahmetx", "/C/Windows/ServiceProfiles/LocalService/x"}
	for _, p := range visible {
		if c.hidden(p) {
			t.Errorf("%s should be visible to ahmet", p)
		}
	}
	for _, p := range hidden {
		if !c.hidden(p) {
			t.Errorf("%s must be hidden from ahmet", p)
		}
	}
	for _, p := range []string{"/C/Users/ahmet", "/C/Users/ahmet/Belgeler", "/C/Muhasebe/2026", "/C/Users/Public/Documents"} {
		if !c.mayRestore(p) {
			t.Errorf("ahmet should be able to restore %s", p)
		}
	}
	for _, p := range []string{"/C/Users", "/C", "/", "/C/Users/bob/x", "/C/Windows/ServiceProfiles", ""} {
		if c.mayRestore(p) {
			t.Errorf("ahmet must not restore %s (it holds someone else's files)", p)
		}
	}
}

func isolationServer(t *testing.T) (*localUIServer, *restoreJobs) {
	t.Helper()
	jobs := fakeRestores(func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	s := &localUIServer{
		Caller:   testCaller,
		Progress: &liveBackupProgress{},
		Restores: jobs,
		ListSnapshot: func(ctx context.Context, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error) {
			return []ports.SnapshotEntry{
				{Name: "ahmet", Type: "dir", Path: "/C/Users/ahmet"},
				{Name: "bob", Type: "dir", Path: "/C/Users/bob"},
				{Name: "Public", Type: "dir", Path: "/C/Users/Public"},
			}, false, nil
		},
		ListSnapshotFull: func(ctx context.Context, snapshotID string) ([]ports.SnapshotEntry, error) {
			return []ports.SnapshotEntry{
				{Name: "maas.xlsx", Type: "file", Path: "/C/Users/bob/Documents/maas.xlsx"},
				{Name: "maas-ahmet.xlsx", Type: "file", Path: "/C/Users/ahmet/Documents/maas-ahmet.xlsx"},
			}, nil
		},
	}
	return s, jobs
}

func serve(s *localUIServer, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body == "" {
		r = localReq(method, path, nil)
	} else {
		r = localReq(method, path, strings.NewReader(body))
	}
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestAnotherAccountsProfileIsNotListedOrBrowsable(t *testing.T) {
	s, _ := isolationServer(t)
	rec := serve(s, "GET", "/api/browse?snapshot=s1&prefix=%2FC%2FUsers", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"bob"`) || !strings.Contains(rec.Body.String(), `"ahmet"`) {
		t.Fatalf("listing = %d %s", rec.Code, rec.Body)
	}
	if rec := serve(s, "GET", "/api/browse?snapshot=s1&prefix=%2FC%2FUsers%2Fbob", ""); rec.Code != 403 {
		t.Fatalf("browsing bob's profile = %d", rec.Code)
	}
}

func TestSearchOnlyFindsTheCallersFiles(t *testing.T) {
	s, _ := isolationServer(t)
	rec := serve(s, "GET", "/api/search?snapshot=s1&q=maas", "")
	if strings.Contains(rec.Body.String(), "/bob/") || !strings.Contains(rec.Body.String(), "maas-ahmet") {
		t.Fatalf("search = %s", rec.Body)
	}
}

func TestRestoringSomeoneElsesFilesOrAWholeDriveIsRefused(t *testing.T) {
	s, _ := isolationServer(t)
	for _, sel := range []string{"/C/Users/bob/Documents/maas.xlsx", "/C/Users", "/C"} {
		body, _ := json.Marshal(map[string]any{"snapshot_id": "s1", "selections": []string{sel}})
		if rec := serve(s, "POST", "/api/restore", string(body)); rec.Code != 403 {
			t.Fatalf("restore %s = %d %s", sel, rec.Code, rec.Body)
		}
	}
	body, _ := json.Marshal(map[string]any{"snapshot_id": "s1", "selections": []string{"/C/Users/ahmet/Documents/maas-ahmet.xlsx"}})
	rec := serve(s, "POST", "/api/restore", string(body))
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), testSID) {
		t.Fatalf("own restore = %d %s", rec.Code, rec.Body)
	}
	var job restoreJob
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	s.Restores.Cancel(job.ID)
	waitState(t, s.Restores, job.ID, restoreCancelled)
}

func TestSomeoneElsesRestoreIsInvisibleAndUntouchable(t *testing.T) {
	s, jobs := isolationServer(t)
	bobs, err := jobs.StartAs("S-1-5-21-1-1002", "s1", []string{"/C/Users/bob/x"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { jobs.Cancel(bobs.ID); waitState(t, jobs, bobs.ID, restoreCancelled) }()
	if rec := serve(s, "GET", "/api/restores", ""); strings.Contains(rec.Body.String(), bobs.ID) {
		t.Fatalf("bob's restore listed to ahmet: %s", rec.Body)
	}
	if rec := serve(s, "GET", "/api/restores/"+bobs.ID, ""); rec.Code != 404 {
		t.Fatalf("get bob's restore = %d", rec.Code)
	}
	if rec := serve(s, "POST", "/api/restores/"+bobs.ID+"/cancel", "{}"); rec.Code == 200 {
		t.Fatal("ahmet cancelled bob's restore")
	}
	if rec := serve(s, "POST", "/api/restores/"+bobs.ID+"/delivered", `{"destination":"C:\\x"}`); rec.Code != 404 {
		t.Fatalf("delivered on bob's restore = %d", rec.Code)
	}
	if rec := serve(s, "GET", "/api/status", ""); strings.Contains(rec.Body.String(), bobs.ID) {
		t.Fatalf("status shows bob's restore: %s", rec.Body)
	}
	if rec := serve(s, "GET", "/api/history", ""); strings.Contains(rec.Body.String(), bobs.ID) {
		t.Fatalf("history shows bob's restore: %s", rec.Body)
	}
}

func TestFolderChangesKeepOtherAccountsFoldersAndCannotAddThem(t *testing.T) {
	var applied []string
	s := &localUIServer{
		Caller:   testCaller,
		Progress: &liveBackupProgress{},
		CurrentFolders: func() ([]string, error) {
			return []string{`C:\Users\ahmet\Belgeler`, `C:\Users\bob\Documents`, `D:\Ortak`}, nil
		},
		ApplyFolders: func(ctx context.Context, roots []string) error { applied = roots; return nil },
	}
	if rec := serve(s, "GET", "/api/folders", ""); strings.Contains(rec.Body.String(), "bob") {
		t.Fatalf("bob's folder shown to ahmet: %s", rec.Body)
	}
	rec := serve(s, "POST", "/api/folders", `{"source_roots":["C:\\Users\\ahmet\\Masaustu","D:\\Ortak"]}`)
	if rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	joined := strings.Join(applied, "|")
	if !strings.Contains(joined, `C:\Users\bob\Documents`) || !strings.Contains(joined, `C:\Users\ahmet\Masaustu`) {
		t.Fatalf("applied = %v", applied)
	}
	if rec := serve(s, "POST", "/api/folders", `{"source_roots":["C:\\Users\\bob\\Desktop"]}`); rec.Code != 403 {
		t.Fatalf("adding bob's folder = %d", rec.Code)
	}
}

func TestUnknownCallerGetsNothing(t *testing.T) {
	for _, s := range []*localUIServer{
		{Progress: &liveBackupProgress{}},
		{Progress: &liveBackupProgress{}, Caller: func(*http.Request) (localCaller, error) { return localCaller{}, errors.New("no") }},
	} {
		for _, path := range []string{"/api/status", "/api/folders", "/api/browse?snapshot=s", "/api/restores", "/api/history"} {
			if rec := serve(s, "GET", path, ""); rec.Code != 403 {
				t.Fatalf("%s without a caller = %d", path, rec.Code)
			}
		}
		if rec := serve(s, "POST", "/api/restore", `{"snapshot_id":"s","selections":["/C/x"]}`); rec.Code != 403 {
			t.Fatalf("restore without a caller = %d", rec.Code)
		}
	}
}
