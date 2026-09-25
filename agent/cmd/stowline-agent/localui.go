package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/application"
	"github.com/canblmz1/stowline/agent/internal/buildinfo"
	"github.com/canblmz1/stowline/agent/internal/config"
	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

//go:embed localui.html
var localUIPage []byte

// localUIScript is the shared UI language layer (see i18n/ and
// scripts/build-i18n.py): English or Turkish, from the Windows UI language.
//
//go:embed i18n.js
var localUIScript []byte

// localUIAddr is fixed (not OS-assigned) so the Stowline Backups app can
// point at it reliably. Bound to loopback only -- nothing outside this
// machine can ever reach it.
const localUIAddr = "127.0.0.1:18080"

// restoreFunc runs one restore into staging and returns the staging path.
type restoreFunc func(ctx context.Context, jobID, snapshot string, selections []string, progress func(domain.RestoreProgress)) (string, error)

// startLocalUIServer wires the real agent-side dependencies into
// localUIServer and starts it in the background. Binding failure (e.g. a
// second agent process already running) is logged and otherwise ignored:
// the local UI is a convenience on top of a backup pipeline that already
// works without it, never a precondition for one.
func startLocalUIServer(progress *liveBackupProgress, eng *restic.Adapter, svcApp *application.Service, exec *ctrlclient.Executor, cfg *config.File, restore restoreFunc) {
	pilotJSONPath := filepath.Join(cfg.PilotRoot, "config", "pilot.json")
	hostname, _ := os.Hostname()
	jobs := &restoreJobs{
		Run:         restore,
		GrantRead:   grantReadTo,
		StagingRoot: cfg.StagingRoot,
		StatePath:   filepath.Join(cfg.PilotRoot, "state", "local-restores.json"),
		ReportDelay: 10 * time.Minute,
	}
	srv := &localUIServer{
		Progress: progress,
		RecentAttempts: func(ctx context.Context, limit int) ([]ports.AttemptRecord, error) {
			if svcApp == nil || svcApp.Journal == nil {
				return nil, nil
			}
			return svcApp.Journal.RecentAttempts(ctx, limit)
		},
		ListSnapshot: func(ctx context.Context, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error) {
			return eng.ListSnapshot(ctx, cfg.Repository, cfg.PasswordRef, snapshotID, prefix)
		},
		ListSnapshotFull: func(ctx context.Context, snapshotID string) ([]ports.SnapshotEntry, error) {
			return eng.ListSnapshotFull(ctx, cfg.Repository, cfg.PasswordRef, snapshotID)
		},
		CurrentFolders: func() ([]string, error) {
			c, err := config.Load(pilotJSONPath)
			if err != nil {
				return nil, err
			}
			return c.SourceRoots, nil
		},
		ApplyFolders: func(ctx context.Context, sourceRoots []string) error {
			_, err := applySelection(ctx, domain.NewUUID(), sourceRoots, nil, pilotJSONPath)
			return err
		},
		Restores: jobs,
		Caller:   resolveLocalCaller,
		Sizes:    &folderSizes{Budget: time.Minute, MaxAge: 10 * time.Minute},
		Computer: map[string]string{"hostname": hostname, "version": buildinfo.Version},
	}
	if exec != nil && exec.Client != nil {
		jobs.Report = func(ctx context.Context, snapshotID string, selections []string, destination string) error {
			return exec.Client.ReportSelfServiceRestore(ctx, snapshotID, selections, destination)
		}
		srv.ReportSelfServiceSelection = func(ctx context.Context, sourceRoots []string) error {
			return exec.Client.ReportSelfServiceSelection(ctx, sourceRoots)
		}
		srv.RequestBackup = exec.Client.RequestSelfServiceBackup
		srv.SendMessage = exec.Client.SendUserMessage
	}
	jobs.load()
	go func() {
		if sha := selfSHA256(); len(sha) >= 8 {
			srv.setComputer("agent_sha", sha[:8])
		}
	}()
	go func() {
		if err := http.ListenAndServe(localUIAddr, srv.Handler()); err != nil {
			fmt.Fprintf(os.Stderr, "local UI server failed to start: %s\n", domain.Redact(err.Error()))
		}
	}()
}

// localUIServer is the API behind Stowline Backups (the end-user app):
// status and live %, folders, self-service restore with progress, history,
// "Şimdi yedekle" and "IT'ye haber ver". Localhost only. Every dependency is
// a plain function so tests can supply fakes without a real repository,
// journal, or control-plane connection.
type localUIServer struct {
	Progress *liveBackupProgress
	// RecentAttempts lists this device's own recent backup attempts, most
	// recent first -- wraps ports.Journal.RecentAttempts.
	RecentAttempts func(ctx context.Context, limit int) ([]ports.AttemptRecord, error)
	// ListSnapshot browses one directory level of one snapshot.
	ListSnapshot func(ctx context.Context, snapshotID, prefix string) ([]ports.SnapshotEntry, bool, error)
	// ListSnapshotFull lists a whole snapshot, for name search.
	ListSnapshotFull func(ctx context.Context, snapshotID string) ([]ports.SnapshotEntry, error)
	// CurrentFolders returns this device's currently configured source roots.
	CurrentFolders func() ([]string, error)
	// ApplyFolders replaces the source root list -- wraps applySelection.
	ApplyFolders func(ctx context.Context, sourceRoots []string) error
	// Restores runs self-service restores in the background.
	Restores *restoreJobs
	// Caller tells which Windows account sent a request (localcaller.go);
	// nil or a failure means the request is refused.
	Caller callerResolver
	// Sizes caches per-folder totals for "Klasörlerim".
	Sizes *folderSizes
	// ReportSelfServiceSelection is best-effort: pilot.json is already
	// updated locally; this only keeps the admin panel's view current.
	ReportSelfServiceSelection func(ctx context.Context, sourceRoots []string) error
	// RequestBackup queues a RUN_BACKUP for this device on the control plane.
	RequestBackup func(ctx context.Context) error
	// SendMessage delivers "IT'ye haber ver" text to the admin's Olaylar.
	SendMessage func(ctx context.Context, message string) error

	computerMu sync.Mutex
	Computer   map[string]string

	searchMu    sync.Mutex
	searchSnap  string
	searchCache []ports.SnapshotEntry
}

func (s *localUIServer) setComputer(k, v string) {
	s.computerMu.Lock()
	defer s.computerMu.Unlock()
	if s.Computer == nil {
		s.Computer = map[string]string{}
	}
	s.Computer[k] = v
}

func (s *localUIServer) computer() map[string]string {
	s.computerMu.Lock()
	defer s.computerMu.Unlock()
	out := map[string]string{}
	for k, v := range s.Computer {
		out[k] = v
	}
	return out
}

func (s *localUIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /i18n.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(localUIScript)
	})
	mux.HandleFunc("GET /api/status", s.withCaller(s.handleStatus))
	mux.HandleFunc("GET /api/folders", s.withCaller(s.handleGetFolders))
	mux.HandleFunc("POST /api/folders", s.withCaller(s.handlePostFolders))
	mux.HandleFunc("GET /api/snapshots", s.withCaller(s.handleSnapshots))
	mux.HandleFunc("GET /api/browse", s.withCaller(s.handleBrowse))
	mux.HandleFunc("GET /api/search", s.withCaller(s.handleSearch))
	mux.HandleFunc("POST /api/restore", s.withCaller(s.handleRestore))
	mux.HandleFunc("GET /api/restores", s.withCaller(s.handleListRestores))
	mux.HandleFunc("GET /api/restores/{id}", s.withCaller(s.handleGetRestore))
	mux.HandleFunc("POST /api/restores/{id}/cancel", s.withCaller(s.handleCancelRestore))
	mux.HandleFunc("POST /api/restores/{id}/delivered", s.withCaller(s.handleDeliveredRestore))
	mux.HandleFunc("POST /api/backup-now", s.withCaller(s.handleBackupNow))
	mux.HandleFunc("POST /api/help", s.withCaller(s.handleHelp))
	mux.HandleFunc("GET /api/history", s.withCaller(s.handleHistory))
	return localOnly(mux)
}

// localUIHosts are the only Host header values the local page answers to.
// Binding to loopback alone is not enough: a website the user has open can
// point its own hostname at 127.0.0.1 (DNS rebinding) and its scripts
// would then reach this server as "same origin". Checking Host closes that.
var localUIHosts = map[string]bool{"127.0.0.1:18080": true, "localhost:18080": true}

var localUIOrigins = map[string]bool{"http://127.0.0.1:18080": true, "http://localhost:18080": true}

// localUIHeader must accompany every state-changing request. A plain HTML
// form on another site cannot set a custom header, and a cross-site fetch
// that sets one triggers a CORS preflight this server never approves -- so
// only the page served from here can change folders or start a restore.
const localUIHeader = "X-Stowline-Local"

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localUIHosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !localUIOrigins[origin] {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Header.Get(localUIHeader) != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func (s *localUIServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(localUIPage)
}

// attemptSummary is the part of a journal attempt's summary the page shows.
type attemptSummary struct {
	TotalFiles int64 `json:"total_files"`
	TotalBytes int64 `json:"total_bytes"`
	DataAdded  int64 `json:"data_added"`
	DurationMS int64 `json:"duration_ms"`
}

func summarize(a ports.AttemptRecord) attemptSummary {
	var raw struct {
		Summary struct {
			DataAdded           int64
			TotalFilesProcessed int64
			TotalBytesProcessed int64
		}
		Duration int64
	}
	_ = json.Unmarshal([]byte(a.SummaryJSON), &raw)
	out := attemptSummary{
		TotalFiles: raw.Summary.TotalFilesProcessed,
		TotalBytes: raw.Summary.TotalBytesProcessed,
		DataAdded:  raw.Summary.DataAdded,
		DurationMS: raw.Duration / int64(time.Millisecond),
	}
	if out.DurationMS == 0 && !a.StartedAt.IsZero() && !a.EndedAt.IsZero() {
		out.DurationMS = a.EndedAt.Sub(a.StartedAt).Milliseconds()
	}
	return out
}

func attemptJSON(a ports.AttemptRecord) map[string]any {
	sum := summarize(a)
	return map[string]any{
		"outcome":     a.Outcome,
		"error_class": a.ErrorClass,
		"started_at":  a.StartedAt,
		"ended_at":    a.EndedAt,
		"snapshot_id": a.SnapshotID,
		"total_files": sum.TotalFiles,
		"total_bytes": sum.TotalBytes,
		"data_added":  sum.DataAdded,
		"duration_ms": sum.DurationMS,
	}
}

// backupPhase names where a running backup is, the same three steps the
// admin panel shows: nothing measured yet, reading files, and restic's 100%
// (every byte read) while the upload is still finishing.
func backupPhase(p *domain.BackupProgress) string {
	switch {
	case p == nil || p.TotalBytes == 0:
		return "preparing"
	case p.BytesDone >= p.TotalBytes:
		return "finalizing"
	default:
		return "scanning"
	}
}

func (s *localUIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.Progress.snapshot()
	body := map[string]any{
		"backing_up": snap != nil,
		"computer":   s.computer(),
	}
	if snap != nil {
		body["phase"] = backupPhase(snap)
		body["percent_done"] = snap.PercentDone
		body["bytes_done"] = snap.BytesDone
		body["total_bytes"] = snap.TotalBytes
		body["files_done"] = snap.FilesDone
		body["total_files"] = snap.TotalFiles
	}
	if s.RecentAttempts != nil {
		if atts, err := s.RecentAttempts(r.Context(), 50); err == nil && len(atts) > 0 {
			body["last_attempt"] = attemptJSON(atts[0])
			for _, a := range atts {
				if a.Outcome == domain.PhaseSucceeded && a.SnapshotID != "" {
					body["last_success"] = attemptJSON(a)
					break
				}
			}
		}
	}
	if s.Restores != nil {
		if cur, ok := s.Restores.Current(); ok && cur.Owner == callerSID(r) {
			body["restore"] = cur
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *localUIServer) foldersBody(roots []string) map[string]any {
	folders := make([]map[string]any, 0, len(roots))
	for _, root := range roots {
		f := map[string]any{"path": root, "name": filepath.Base(filepath.Clean(root))}
		if s.Sizes != nil {
			size := s.Sizes.Get(root)
			f["bytes"], f["files"], f["complete"], f["pending"] = size.Bytes, size.Files, size.Complete, size.Pending
		}
		folders = append(folders, f)
	}
	return map[string]any{"source_roots": roots, "folders": folders}
}

func (s *localUIServer) handleGetFolders(w http.ResponseWriter, r *http.Request) {
	if s.CurrentFolders == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	roots, err := s.CurrentFolders()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c, _ := callerFrom(r.Context())
	visible := []string{}
	for _, root := range roots {
		if !c.hidden(root) {
			visible = append(visible, root)
		}
	}
	writeJSON(w, http.StatusOK, s.foldersBody(visible))
}

type foldersRequest struct {
	SourceRoots        []string `json:"source_roots"`
	ConfirmedSensitive []string `json:"confirmed_sensitive"`
}

func (s *localUIServer) handlePostFolders(w http.ResponseWriter, r *http.Request) {
	if s.ApplyFolders == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	var body foldersRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	confirmed := map[string]bool{}
	for _, c := range body.ConfirmedSensitive {
		confirmed[c] = true
	}
	var needsConfirmation []string
	for _, root := range body.SourceRoots {
		if isSensitivePath(root) && !confirmed[root] {
			needsConfirmation = append(needsConfirmation, root)
		}
	}
	if len(needsConfirmation) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":              "sensitive_confirmation_required",
			"needs_confirmation": needsConfirmation,
		})
		return
	}
	// The page only shows (and so only sends) this person's folders; other
	// accounts' folders stay in the backup exactly as they were.
	c, _ := callerFrom(r.Context())
	var others []string
	if s.CurrentFolders != nil {
		if current, err := s.CurrentFolders(); err == nil {
			for _, root := range current {
				if c.hidden(root) {
					others = append(others, root)
				}
			}
		}
	}
	for _, root := range body.SourceRoots {
		if c.hidden(root) {
			writeError(w, http.StatusForbidden, "not_your_folder")
			return
		}
	}
	roots := append(append([]string(nil), body.SourceRoots...), others...)
	if err := s.ApplyFolders(r.Context(), roots); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if s.ReportSelfServiceSelection != nil {
		go func() { _ = s.ReportSelfServiceSelection(context.Background(), roots) }()
	}
	writeJSON(w, http.StatusOK, s.foldersBody(body.SourceRoots))
}

func (s *localUIServer) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if s.RecentAttempts == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	atts, err := s.RecentAttempts(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snaps := []map[string]any{}
	for _, a := range atts {
		if a.SnapshotID == "" || a.Outcome != domain.PhaseSucceeded {
			continue
		}
		sum := summarize(a)
		snaps = append(snaps, map[string]any{
			"snapshot_id": a.SnapshotID,
			"ended_at":    a.EndedAt,
			"total_files": sum.TotalFiles,
			"total_bytes": sum.TotalBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

func (s *localUIServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if s.ListSnapshot == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	snapshotID := r.URL.Query().Get("snapshot")
	prefix := r.URL.Query().Get("prefix")
	if strings.TrimSpace(snapshotID) == "" {
		writeError(w, http.StatusBadRequest, "snapshot is required")
		return
	}
	c, _ := callerFrom(r.Context())
	if prefix != "" && c.hidden(prefix) {
		writeError(w, http.StatusForbidden, "not_your_folder")
		return
	}
	entries, truncated, err := s.ListSnapshot(r.Context(), snapshotID, prefix)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	visible := make([]ports.SnapshotEntry, 0, len(entries))
	for _, e := range entries {
		if !c.hidden(e.Path) {
			visible = append(visible, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": visible, "prefix": prefix, "truncated": truncated})
}

const maxSearchResults = 200

// handleSearch finds files by name in one snapshot. The full listing of the
// most recently searched snapshot is cached, so typing does not re-run
// restic for every keystroke.
func (s *localUIServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.ListSnapshotFull == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	snapshotID := strings.TrimSpace(r.URL.Query().Get("snapshot"))
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if snapshotID == "" || len([]rune(q)) < 2 {
		writeError(w, http.StatusBadRequest, "snapshot and a query of at least 2 characters are required")
		return
	}
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if s.searchSnap != snapshotID {
		all, err := s.ListSnapshotFull(r.Context(), snapshotID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.searchSnap, s.searchCache = snapshotID, all
	}
	c, _ := callerFrom(r.Context())
	results := []ports.SnapshotEntry{}
	truncated := false
	for _, e := range s.searchCache {
		if e.Type == "dir" || !strings.Contains(strings.ToLower(e.Name), q) || c.hidden(e.Path) {
			continue
		}
		if len(results) == maxSearchResults {
			truncated = true
			break
		}
		results = append(results, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "truncated": truncated})
}

type restoreRequest struct {
	SnapshotID string   `json:"snapshot_id"`
	Selections []string `json:"selections"`
}

func (s *localUIServer) handleRestore(w http.ResponseWriter, r *http.Request) {
	if s.Restores == nil || s.Restores.Run == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return
	}
	var body restoreRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.SnapshotID) == "" || len(body.Selections) == 0 {
		writeError(w, http.StatusBadRequest, "snapshot_id and at least one selection are required")
		return
	}
	c, _ := callerFrom(r.Context())
	for _, sel := range body.Selections {
		if !c.mayRestore(sel) {
			writeError(w, http.StatusForbidden, "not_your_folder")
			return
		}
	}
	job, err := s.Restores.StartAs(c.SID, body.SnapshotID, body.Selections)
	if errors.Is(err, errRestoreBusy) {
		writeError(w, http.StatusConflict, "restore_busy")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *localUIServer) handleListRestores(w http.ResponseWriter, r *http.Request) {
	if s.Restores == nil {
		writeJSON(w, http.StatusOK, map[string]any{"restores": []restoreJob{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restores": s.Restores.ListFor(callerSID(r))})
}

func (s *localUIServer) handleGetRestore(w http.ResponseWriter, r *http.Request) {
	if s.Restores == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	job, ok := s.Restores.Get(r.PathValue("id"))
	if !ok || job.Owner != callerSID(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *localUIServer) handleCancelRestore(w http.ResponseWriter, r *http.Request) {
	if s.Restores == nil {
		writeError(w, http.StatusConflict, "not running")
		return
	}
	if job, ok := s.Restores.Get(r.PathValue("id")); !ok || job.Owner != callerSID(r) || !s.Restores.Cancel(job.ID) {
		writeError(w, http.StatusConflict, "not running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelling": true})
}

type deliveredRequest struct {
	Destination string `json:"destination"`
}

func (s *localUIServer) handleDeliveredRestore(w http.ResponseWriter, r *http.Request) {
	if s.Restores == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if job, ok := s.Restores.Get(r.PathValue("id")); !ok || job.Owner != callerSID(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body deliveredRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Destination) == "" {
		writeError(w, http.StatusBadRequest, "destination is required")
		return
	}
	if err := s.Restores.Delivered(r.PathValue("id"), body.Destination); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	job, _ := s.Restores.Get(r.PathValue("id"))
	writeJSON(w, http.StatusOK, job)
}

func (s *localUIServer) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if s.RequestBackup == nil {
		writeError(w, http.StatusServiceUnavailable, "not_connected")
		return
	}
	if s.Progress.snapshot() != nil {
		writeError(w, http.StatusConflict, "already_running")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.RequestBackup(ctx); err != nil {
		writeError(w, http.StatusBadGateway, domain.Redact(err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type helpRequest struct {
	Message string `json:"message"`
}

func (s *localUIServer) handleHelp(w http.ResponseWriter, r *http.Request) {
	if s.SendMessage == nil {
		writeError(w, http.StatusServiceUnavailable, "not_connected")
		return
	}
	var body helpRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" || len([]rune(msg)) > 2000 {
		writeError(w, http.StatusBadRequest, "message must be 1..2000 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.SendMessage(ctx, msg); err != nil {
		writeError(w, http.StatusBadGateway, domain.Redact(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// handleHistory merges recent backups and self-service restores, newest first.
func (s *localUIServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	type item struct {
		at   time.Time
		body map[string]any
	}
	var items []item
	if s.RecentAttempts != nil {
		if atts, err := s.RecentAttempts(r.Context(), 30); err == nil {
			for _, a := range atts {
				if a.Kind != "" && a.Kind != domain.JobBackup {
					continue
				}
				b := attemptJSON(a)
				b["kind"] = "backup"
				at := a.EndedAt
				if at.IsZero() {
					at = a.StartedAt
				}
				items = append(items, item{at, b})
			}
		}
	}
	if s.Restores != nil {
		for _, j := range s.Restores.ListFor(callerSID(r)) {
			at := j.EndedAt
			if at.IsZero() {
				at = j.StartedAt
			}
			items = append(items, item{at, map[string]any{"kind": "restore", "restore": j}})
		}
	}
	sort.SliceStable(items, func(i, k int) bool { return items[i].at.After(items[k].at) })
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, it.body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
