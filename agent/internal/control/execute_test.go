package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	svcresult "github.com/canblmz1/stowline/agent/internal/windows/service"
)

func TestDispatchRejectsUnknown(t *testing.T) {
	e := &Executor{}
	for _, kind := range []string{"EXEC", "SHELL", "POWERSHELL", "CMD", "FORGET", "PRUNE", "REPAIR", "UNLOCK", "KEY", "QUALIFICATION_BACKUP", "RUN_QUALIFICATION", "BACKUP"} {
		if _, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: kind}); err == nil {
			t.Fatalf("%s must fail", kind)
		}
	}
}

// Reproduces the live finding: an operator-triggered RUN_BACKUP declined by
// WAN admission must surface its exact (fixed-vocabulary, non-secret) reason
// in both the ack's result_json (so the admin UI can show it) and the
// PollError's diagnostic string (so Event Log shows it without needing to
// query the admin API at all).
func TestDispatchRunBackupSurfacesWANAdmissionDeclineReason(t *testing.T) {
	e := &Executor{Backup: func(context.Context) error {
		return &domain.WANAdmissionDeclinedError{Reason: "SITE_CAPACITY_WAIT"}
	}}
	extra, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "RUN_BACKUP"})
	var wanErr *domain.WANAdmissionDeclinedError
	if !errors.As(err, &wanErr) || wanErr.Reason != "SITE_CAPACITY_WAIT" {
		t.Fatalf("want WANAdmissionDeclinedError with reason SITE_CAPACITY_WAIT, got %v", err)
	}
	if !errors.Is(err, domain.ErrWANAdmissionDeclined) {
		t.Fatal("must still match the sentinel via errors.Is")
	}
	if extra["reason"] != "SITE_CAPACITY_WAIT" || extra["error_class"] != "RESOURCE_EXHAUSTED" {
		t.Fatalf("ack extra must carry the reason for the admin UI: %v", extra)
	}
	pe := newPollError(StageDispatch, EventDispatchFinished, &CommandEnvelope{CommandID: "cmd-1", Kind: "RUN_BACKUP"}, err)
	if !strings.Contains(pe.Error(), "reason=SITE_CAPACITY_WAIT") {
		t.Fatalf("diagnostic string must carry the reason: %s", pe.Error())
	}
}

// Reproduces the live finding: a VSS-inconsistent operator-triggered
// RUN_BACKUP (a real snapshot exists, but restic's VSS evidence did not
// positively confirm all required volumes, so Service.Backup correctly
// returns PhasePartial/ErrorConsistencyNotMet, never a faked SUCCEEDED) was
// still surfaced as error_class=INTERNAL in both the command ack's extra
// and the Event Log diagnostic string -- because neither previously knew
// how to read the already-correct classification out of the *TickError
// winsvc.WrapOperatorBackup returns for any non-nil outcome.
func TestDispatchRunBackupSurfacesConsistencyNotMetNotGenericInternal(t *testing.T) {
	e := &Executor{Backup: func(context.Context) error {
		return &svcresult.TickError{
			Outcome:   domain.PhasePartial,
			Class:     domain.ErrorConsistencyNotMet,
			AttemptID: "att-vss-1",
		}
	}}
	extra, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "RUN_BACKUP"})
	if extra["error_class"] != string(domain.ErrorConsistencyNotMet) {
		t.Fatalf("ack extra must carry the real classification, not be empty: %v", extra)
	}
	pe := newPollError(StageDispatch, EventDispatchFinished, &CommandEnvelope{CommandID: "cmd-1", Kind: "RUN_BACKUP"}, err)
	if !strings.Contains(pe.Error(), "error_class=CONSISTENCY_NOT_MET") {
		t.Fatalf("diagnostic string must not fall back to INTERNAL: %s", pe.Error())
	}
}

func TestDispatchRoutesBrowseLocalDirAndAppliesLimitDefault(t *testing.T) {
	var gotPath, gotCursor string
	var gotLimit int
	e := &Executor{BrowseLocalDir: func(_ context.Context, path, cursor string, limit int) (map[string]any, error) {
		gotPath, gotCursor, gotLimit = path, cursor, limit
		return map[string]any{"path": path}, nil
	}}
	_, err := e.dispatch(context.Background(), &CommandEnvelope{
		Kind:    "BROWSE_LOCAL_DIR",
		Payload: map[string]any{"path": `C:\Users\op\Desktop`, "cursor": "resume-here", "limit": float64(50)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != `C:\Users\op\Desktop` || gotCursor != "resume-here" || gotLimit != 50 {
		t.Fatalf("got path=%q cursor=%q limit=%d", gotPath, gotCursor, gotLimit)
	}
}

func TestDispatchBrowseLocalDirMissingExecutorFailsClosed(t *testing.T) {
	e := &Executor{}
	if _, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "BROWSE_LOCAL_DIR", Payload: map[string]any{"path": `C:\`}}); err == nil {
		t.Fatal("must fail without a wired BrowseLocalDir executor")
	}
}

func TestDispatchRoutesApplySelectionWithParsedPayload(t *testing.T) {
	var gotRev string
	var gotRoots, gotConsents []string
	e := &Executor{ApplySelection: func(_ context.Context, revisionID string, sourceRoots, sensitiveConsents []string) (map[string]any, error) {
		gotRev, gotRoots, gotConsents = revisionID, sourceRoots, sensitiveConsents
		return map[string]any{"revision_id": revisionID}, nil
	}}
	_, err := e.dispatch(context.Background(), &CommandEnvelope{
		Kind: "APPLY_SELECTION",
		Payload: map[string]any{
			"revision_id":        "rev-1",
			"source_roots":       []any{`C:\Users\op\Desktop`, `C:\Users\op\Documents`},
			"sensitive_consents": []any{`C:\Users\op\Desktop\.env`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotRev != "rev-1" || len(gotRoots) != 2 || len(gotConsents) != 1 {
		t.Fatalf("got rev=%q roots=%v consents=%v", gotRev, gotRoots, gotConsents)
	}
}

func TestDispatchApplySelectionMissingExecutorFailsClosed(t *testing.T) {
	e := &Executor{}
	if _, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "APPLY_SELECTION", Payload: map[string]any{}}); err == nil {
		t.Fatal("must fail without a wired ApplySelection executor")
	}
}

func TestOutboxMapper(t *testing.T) {
	raw, _ := json.Marshal(domain.BackupResult{Outcome: domain.PhaseSucceeded, SnapshotID: "aa", PublishedNotGreen: false})
	when := time.Date(2026, 9, 9, 18, 54, 26, 0, time.UTC)
	m := outboxToAttempt(string(raw), "att-1", when)
	if m["outcome"] != "SUCCEEDED" {
		t.Fatalf("%v", m)
	}
	if m["attempt_id"] != "att-1" {
		t.Fatalf("%v", m)
	}
	// Regression: the server uses this to refuse moving a device's
	// last-success state backward when a delayed report (e.g. from a
	// week-stuck outbox retry) arrives after a newer attempt already has.
	// Without it, every backlogged report looks like it just happened.
	got, ok := m["attempt_ended_at"].(string)
	if !ok {
		t.Fatalf("attempt_ended_at missing or not a string: %v", m)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("attempt_ended_at not RFC3339: %v (%v)", got, err)
	}
	if !parsed.Equal(when) {
		t.Fatalf("attempt_ended_at = %v, want %v (the outbox event's own CreatedAt, not now)", parsed, when)
	}
}

func TestOutboxMapperOmitsAttemptEndedAtWhenCreatedAtIsZero(t *testing.T) {
	raw, _ := json.Marshal(domain.BackupResult{Outcome: domain.PhaseSucceeded, SnapshotID: "aa"})
	m := outboxToAttempt(string(raw), "att-1", time.Time{})
	if _, ok := m["attempt_ended_at"]; ok {
		t.Fatalf("must omit attempt_ended_at rather than send a zero/garbage time: %v", m)
	}
}

type pollHarness struct {
	backup    atomic.Int32
	acks      []string
	ackExtras []map[string]any
	failAcksN int
}

func (h *pollHarness) server(t *testing.T, cmd *CommandEnvelope) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/heartbeat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"poll_seconds":20}`))
		case r.URL.Path == "/api/v1/agent/work/claim":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "command": cmd})
		case strings.HasPrefix(r.URL.Path, "/api/v1/agent/commands/"):
			if h.failAcksN > 0 {
				h.failAcksN--
				w.WriteHeader(500)
				return
			}
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if s, _ := body["state"].(string); s != "" {
				h.acks = append(h.acks, s)
				extra, _ := body["extra"].(map[string]any)
				h.ackExtras = append(h.ackExtras, extra)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

// flakyJournal wraps a real journal and injects a fixed number of
// FinishCommand failures before delegating normally -- simulates a local
// disk/SQLite hiccup between a command's handler finishing and its result
// being durably recorded.
type flakyJournal struct {
	*journal.SQLite
	failFinishN int
}

func (f *flakyJournal) FinishCommand(ctx context.Context, commandID, state string, extra map[string]any) error {
	if f.failFinishN > 0 {
		f.failFinishN--
		return errors.New("injected finish failure")
	}
	return f.SQLite.FinishCommand(ctx, commandID, state, extra)
}

func TestPollDoesNotInventSucceededAfterCrashBeforeFinish(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if _, err := j.ConsumeCommand(ctx, "cmd-1", "job-1", false); err != nil {
		t.Fatal(err)
	}
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-1", JobID: "job-1", Kind: "RUN_BACKUP"}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		Backup: func(context.Context) error {
			h.backup.Add(1)
			return nil
		},
	}
	if err := e.Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if h.backup.Load() != 1 {
		t.Fatalf("non-terminal consume must re-dispatch, got %d", h.backup.Load())
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("acks=%v", h.acks)
	}
}

func TestPollReplayOfTerminalFailedDoesNotGreen(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if _, err := j.ConsumeCommand(ctx, "cmd-2", "job-2", false); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishCommand(ctx, "cmd-2", "FAILED", nil); err != nil {
		t.Fatal(err)
	}
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-2", JobID: "job-2", Kind: "RUN_BACKUP"}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		Backup: func(context.Context) error {
			h.backup.Add(1)
			return nil
		},
	}
	if err := e.Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if h.backup.Load() != 0 {
		t.Fatal("terminal command must not re-execute")
	}
	if len(h.acks) != 1 || h.acks[0] != "FAILED" {
		t.Fatalf("must replay recorded FAILED, acks=%v", h.acks)
	}
}

func TestPollNonTerminalEmptyResultDoesNotAckSucceeded(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if _, err := j.ConsumeCommand(ctx, "cmd-3", "job-3", true); err != nil {
		t.Fatal(err)
	}
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-3", JobID: "job-3", Kind: "RUN_BACKUP"}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		Backup: func(context.Context) error {
			h.backup.Add(1)
			return nil
		},
	}
	if err := e.Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if h.backup.Load() != 0 {
		t.Fatal("terminal empty result must not re-run")
	}
	if len(h.acks) != 1 || h.acks[0] != "FAILED" {
		t.Fatalf("empty terminal result must ACK FAILED, acks=%v", h.acks)
	}
}

// Reproduces the live qualification finding: BROWSE_LOCAL_DIR's handler
// succeeds and the result is durably recorded, but the ack HTTP call
// itself fails once (dropped connection, control-plane hiccup). Poll must
// surface this as a stage=ack PollError instead of swallowing it, and a
// later redelivery of the same command must replay the exact recorded
// "extra" (the browse listing) rather than losing it or re-running the
// handler.
func TestPollBrowseAckTransportFailureThenRedeliveryReplaysExtra(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	h := &pollHarness{failAcksN: 1}
	cmd := &CommandEnvelope{CommandID: "cmd-browse-1", JobID: "job-1", Kind: "BROWSE_LOCAL_DIR", Payload: map[string]any{"path": `C:\x`}}
	srv := h.server(t, cmd)
	defer srv.Close()
	var browseCalls atomic.Int32
	newExecutor := func() *Executor {
		return &Executor{
			Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
			Journal: j,
			BrowseLocalDir: func(context.Context, string, string, int) (map[string]any, error) {
				browseCalls.Add(1)
				return map[string]any{"path": `C:\x`, "entries": []any{"a.txt"}}, nil
			},
		}
	}

	err1 := newExecutor().Poll(ctx, "none")
	var pe *PollError
	if !errors.As(err1, &pe) || pe.Stage != StageAck {
		t.Fatalf("want stage=ack PollError, got %v", err1)
	}
	if len(h.acks) != 0 {
		t.Fatalf("failed ack attempt must not be recorded as sent: %v", h.acks)
	}

	// A fresh Executor stands in for the agent process having restarted;
	// the durable journal is the only thing carried over, exactly as it
	// would be on disk across a real restart.
	if err := newExecutor().Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("acks=%v", h.acks)
	}
	if browseCalls.Load() != 1 {
		t.Fatalf("terminal browse result must replay without re-dispatch, calls=%d", browseCalls.Load())
	}
	if h.ackExtras[0]["path"] != `C:\x` {
		t.Fatalf("browse result lost on replay: %v", h.ackExtras[0])
	}
	entries, _ := h.ackExtras[0]["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("browse entries lost on replay: %v", h.ackExtras[0])
	}
}

// Same fix, APPLY_SELECTION's shape: its "extra" is what confirms the
// revision that was actually applied, which is exactly the payload a lost
// ack would silently drop.
func TestPollApplySelectionAckTransportFailureThenRedeliveryReplaysExtra(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	h := &pollHarness{failAcksN: 1}
	cmd := &CommandEnvelope{CommandID: "cmd-apply-1", JobID: "job-2", Kind: "APPLY_SELECTION", Payload: map[string]any{"revision_id": "rev-1", "source_roots": []any{`C:\Users\op\Desktop`}}}
	srv := h.server(t, cmd)
	defer srv.Close()
	var applyCalls atomic.Int32
	newExecutor := func() *Executor {
		return &Executor{
			Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
			Journal: j,
			ApplySelection: func(_ context.Context, revisionID string, sourceRoots, _ []string) (map[string]any, error) {
				applyCalls.Add(1)
				return map[string]any{"revision_id": revisionID, "source_roots": sourceRoots}, nil
			},
		}
	}

	err1 := newExecutor().Poll(ctx, "none")
	var pe *PollError
	if !errors.As(err1, &pe) || pe.Stage != StageAck {
		t.Fatalf("want stage=ack PollError, got %v", err1)
	}

	if err := newExecutor().Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("acks=%v", h.acks)
	}
	if applyCalls.Load() != 1 {
		t.Fatalf("terminal apply result must replay without re-dispatch, calls=%d", applyCalls.Load())
	}
	if h.ackExtras[0]["revision_id"] != "rev-1" {
		t.Fatalf("applied revision_id lost on replay: %v", h.ackExtras[0])
	}
}

// The other half of the same bug: the handler runs fine but the LOCAL
// durable write of its result fails once (e.g. a transient SQLite
// contention). Poll must not attempt the remote ack against a result it
// never durably recorded, and a later poll must safely re-run the
// (idempotent, read-only) handler and complete normally.
func TestPollFinishCommandTransientFailureRecoversOnRedispatch(t *testing.T) {
	real, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	j := &flakyJournal{SQLite: real, failFinishN: 1}
	ctx := context.Background()
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-browse-2", JobID: "job-3", Kind: "BROWSE_LOCAL_DIR", Payload: map[string]any{"path": `C:\y`}}
	srv := h.server(t, cmd)
	defer srv.Close()
	var browseCalls atomic.Int32
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		BrowseLocalDir: func(context.Context, string, string, int) (map[string]any, error) {
			browseCalls.Add(1)
			return map[string]any{"path": `C:\y`}, nil
		},
	}

	err1 := e.Poll(ctx, "none")
	var pe *PollError
	if !errors.As(err1, &pe) || pe.Stage != StageFinishCommand {
		t.Fatalf("want stage=finish_command PollError, got %v", err1)
	}
	if len(h.acks) != 0 {
		t.Fatalf("ack must not be attempted when the local result was never durably recorded: %v", h.acks)
	}

	if err := e.Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if browseCalls.Load() != 2 {
		t.Fatalf("re-dispatch after a finish failure must safely re-run the handler, got %d calls", browseCalls.Load())
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("acks=%v", h.acks)
	}
}

func TestPollBrowseSuccessPersistsExactResultBeforeAck(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-browse-ok", JobID: "job-browse-ok", Kind: "BROWSE_LOCAL_DIR", Payload: map[string]any{"path": `C:\safe`}}
	srv := h.server(t, cmd)
	defer srv.Close()
	var events []CommandDiagnostic
	want := map[string]any{"path": `C:\safe`, "entries": []any{map[string]any{"name": "one.txt", "type": "file"}}}
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		BrowseLocalDir: func(context.Context, string, string, int) (map[string]any, error) {
			return want, nil
		},
		OnCommandEvent: func(event CommandDiagnostic) { events = append(events, event) },
	}
	if err := e.Poll(context.Background(), "none"); err != nil {
		t.Fatal(err)
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" || h.ackExtras[0]["path"] != want["path"] {
		t.Fatalf("ack did not contain exact browse result: states=%v extras=%v", h.acks, h.ackExtras)
	}
	terminal, state, extra, err := j.CommandResult(context.Background(), cmd.CommandID)
	if err != nil || !terminal || state != "SUCCEEDED" || extra["path"] != want["path"] {
		t.Fatalf("durable result missing before ack: terminal=%v state=%q extra=%v err=%v", terminal, state, extra, err)
	}
	wantEvents := []CommandEvent{EventDispatchStarted, EventDispatchFinished, EventAckStarted, EventAckSucceeded}
	if len(events) != len(wantEvents) {
		t.Fatalf("diagnostic events=%v", events)
	}
	for i, wantEvent := range wantEvents {
		if events[i].Event != wantEvent {
			t.Fatalf("event[%d]=%s want %s (all=%v)", i, events[i].Event, wantEvent, events)
		}
	}
}

func TestPollApplySelectionSuccessPersistsExactResultBeforeAck(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-apply-ok", JobID: "job-apply-ok", Kind: "APPLY_SELECTION", Payload: map[string]any{
		"revision_id": "rev-ok", "source_roots": []any{`C:\Synthetic`},
	}}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		ApplySelection: func(_ context.Context, revisionID string, roots, _ []string) (map[string]any, error) {
			return map[string]any{"revision_id": revisionID, "source_roots": roots}, nil
		},
	}
	if err := e.Poll(context.Background(), "none"); err != nil {
		t.Fatal(err)
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" || h.ackExtras[0]["revision_id"] != "rev-ok" {
		t.Fatalf("apply ack=%v extra=%v", h.acks, h.ackExtras)
	}
}

func TestPollTerminalBrowseResultSurvivesProcessRestartBeforeAck(t *testing.T) {
	testTerminalReplayAfterRestart(t, "BROWSE_LOCAL_DIR", "cmd-browse-restart", map[string]any{
		"path": `C:\Synthetic`, "entries": []any{"one.txt"},
	})
}

func TestPollTerminalApplyResultSurvivesProcessRestartBeforeAck(t *testing.T) {
	testTerminalReplayAfterRestart(t, "APPLY_SELECTION", "cmd-apply-restart", map[string]any{
		"revision_id": "rev-restart", "source_roots": []any{`C:\Synthetic`},
	})
}

func testTerminalReplayAfterRestart(t *testing.T, kind, commandID string, extra map[string]any) {
	t.Helper()
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	if _, err := j.ConsumeCommand(ctx, commandID, "job-restart", false); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishCommand(ctx, commandID, "SUCCEEDED", extra); err != nil {
		t.Fatal(err)
	}
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: commandID, JobID: "job-restart", Kind: kind}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		BrowseLocalDir: func(context.Context, string, string, int) (map[string]any, error) {
			t.Fatal("terminal browse command was re-dispatched after restart")
			return nil, nil
		},
		ApplySelection: func(context.Context, string, []string, []string) (map[string]any, error) {
			t.Fatal("terminal apply command was re-dispatched after restart")
			return nil, nil
		},
	}
	if err := e.Poll(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	if len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("acks=%v", h.acks)
	}
	for key, want := range extra {
		got := h.ackExtras[0][key]
		if key == "entries" || key == "source_roots" {
			if len(got.([]any)) != len(want.([]any)) {
				t.Fatalf("replayed %s lost: got=%v want=%v", key, got, want)
			}
			continue
		}
		if got != want {
			t.Fatalf("replayed %s=%v want %v", key, got, want)
		}
	}
}

func TestPollApplySelectionFinishCommandTransientFailureRecovers(t *testing.T) {
	real, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	j := &flakyJournal{SQLite: real, failFinishN: 1}
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-apply-finish", JobID: "job-apply-finish", Kind: "APPLY_SELECTION", Payload: map[string]any{
		"revision_id": "rev-finish", "source_roots": []any{`C:\Synthetic`},
	}}
	srv := h.server(t, cmd)
	defer srv.Close()
	var applyCalls atomic.Int32
	var events []CommandDiagnostic
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		ApplySelection: func(_ context.Context, revisionID string, roots, _ []string) (map[string]any, error) {
			applyCalls.Add(1)
			return map[string]any{"revision_id": revisionID, "source_roots": roots}, nil
		},
		OnCommandEvent: func(event CommandDiagnostic) { events = append(events, event) },
	}
	err = e.Poll(context.Background(), "none")
	var pe *PollError
	if !errors.As(err, &pe) || pe.Stage != StageFinishCommand || pe.Event != EventJournalFinishFailed {
		t.Fatalf("want JOURNAL_FINISH_FAILED, got %v", err)
	}
	if len(h.acks) != 0 {
		t.Fatalf("result must be durable before ack: %v", h.acks)
	}
	if err := e.Poll(context.Background(), "none"); err != nil {
		t.Fatal(err)
	}
	if applyCalls.Load() != 2 || len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
		t.Fatalf("calls=%d acks=%v", applyCalls.Load(), h.acks)
	}
	found := false
	for _, event := range events {
		if event.Event == EventJournalFinishFailed {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing deterministic journal failure marker: %v", events)
	}
}

func TestPollFailedCommandDurablyAcksFailed(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-failed", JobID: "job-failed", Kind: "BROWSE_LOCAL_DIR", Payload: map[string]any{"path": `C:\denied`}}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		BrowseLocalDir: func(context.Context, string, string, int) (map[string]any, error) {
			return nil, domain.ErrBrowsePathRejected
		},
	}
	err = e.Poll(context.Background(), "none")
	var pe *PollError
	if !errors.As(err, &pe) || pe.Stage != StageDispatch || pe.Class != domain.ErrorPathSafety {
		t.Fatalf("want normalized dispatch failure, got %v", err)
	}
	if len(h.acks) != 1 || h.acks[0] != "FAILED" {
		t.Fatalf("failed command must ACK FAILED, got %v", h.acks)
	}
	terminal, state, _, resultErr := j.CommandResult(context.Background(), cmd.CommandID)
	if resultErr != nil || !terminal || state != "FAILED" {
		t.Fatalf("failed result not durable: terminal=%v state=%q err=%v", terminal, state, resultErr)
	}
}

func TestPollCommandRegressionMatrix(t *testing.T) {
	tests := []struct {
		kind    string
		payload map[string]any
		wire    func(*Executor, *atomic.Int32)
	}{
		{"RUN_CANARY", nil, func(e *Executor, calls *atomic.Int32) {
			e.Canary = func(context.Context) error { calls.Add(1); return nil }
		}},
		{"BROWSE_SNAPSHOT", map[string]any{"snapshot_id": strings.Repeat("a", 64), "prefix": "/C"}, func(e *Executor, calls *atomic.Int32) {
			e.Browse = func(context.Context, string, string) (map[string]any, error) {
				calls.Add(1)
				return map[string]any{"listing": "[]"}, nil
			}
		}},
		{"RESTORE_TO_STAGING", map[string]any{"snapshot_id": strings.Repeat("b", 64), "destination_mode": "STAGING", "selections": []any{"/C/Synthetic"}}, func(e *Executor, calls *atomic.Int32) {
			e.Restore = func(context.Context, string, string, []string) error { calls.Add(1); return nil }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			h := &pollHarness{}
			cmd := &CommandEnvelope{CommandID: "cmd-" + strings.ToLower(tc.kind), JobID: "job-regression", Kind: tc.kind, Payload: tc.payload}
			srv := h.server(t, cmd)
			defer srv.Close()
			e := &Executor{Client: &Client{BaseURL: srv.URL, Credential: "tok"}, Journal: j}
			var calls atomic.Int32
			tc.wire(e, &calls)
			if err := e.Poll(context.Background(), "none"); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || len(h.acks) != 1 || h.acks[0] != "SUCCEEDED" {
				t.Fatalf("calls=%d acks=%v", calls.Load(), h.acks)
			}
		})
	}
}

func TestPollErrorMessageIsNormalizedAndDoesNotExposeRawError(t *testing.T) {
	raw := errors.New("Bearer super-secret password=hunter2 file-body-marker")
	err := newPollError(StageAck, EventAckFailed, &CommandEnvelope{CommandID: "cmd-safe", Kind: "BROWSE_LOCAL_DIR"}, raw)
	message := err.Error()
	if strings.Contains(message, "super-secret") || strings.Contains(message, "hunter2") || strings.Contains(message, "file-body-marker") {
		t.Fatalf("raw error leaked into operator log: %s", message)
	}
	for _, want := range []string{"event=ACK_FAILED", "stage=ack", "command_id=cmd-safe", "kind=BROWSE_LOCAL_DIR", "error_class=NETWORK"} {
		if !strings.Contains(message, want) {
			t.Fatalf("normalized diagnostic missing %q: %s", want, message)
		}
	}
	if !errors.Is(err, raw) {
		t.Fatal("raw cause must remain available to errors.Is without being logged")
	}
}

func TestPollUnknownCommandFailsClosedBeforeDispatch(t *testing.T) {
	h := &pollHarness{}
	cmd := &CommandEnvelope{CommandID: "cmd-unknown", JobID: "job-unknown", Kind: "POWERSHELL", Payload: map[string]any{"content": "must-not-run"}}
	srv := h.server(t, cmd)
	defer srv.Close()
	e := &Executor{Client: &Client{BaseURL: srv.URL, Credential: "tok"}}
	err := e.Poll(context.Background(), "none")
	var pe *PollError
	if !errors.As(err, &pe) || pe.Stage != StageClaim || pe.Class != domain.ErrorConfig {
		t.Fatalf("unknown command must fail closed at claim validation: %v", err)
	}
	if pe.CommandID != cmd.CommandID || pe.Kind != cmd.Kind {
		t.Fatalf("rejected command diagnostics lost id/kind: %+v", pe)
	}
	if len(h.acks) != 0 {
		t.Fatalf("unknown command must never be acknowledged as executed: %v", h.acks)
	}
}

func TestFlushOutboxDispatchesCatalogBuildEventsSeparatelyFromAttemptReports(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var reportedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reportedPaths = append(reportedPaths, r.URL.Path)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	var builtSnapshots []string
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		BuildCatalog: func(ctx context.Context, snapshotID string) error {
			builtSnapshots = append(builtSnapshots, snapshotID)
			return nil
		},
	}

	terminalPayload, _ := json.Marshal(map[string]any{"outcome": "SUCCEEDED", "snapshot_id": strings.Repeat("a", 64)})
	catalogPayload, _ := json.Marshal(map[string]string{"snapshot_id": strings.Repeat("a", 64)})
	if err := j.AppendOutbox(context.Background(), ports.OutboxEvent{EventID: "ev-terminal", AttemptID: "att-1", Type: "backup.terminal", Payload: string(terminalPayload), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendOutbox(context.Background(), ports.OutboxEvent{EventID: "ev-catalog", AttemptID: "att-1", Type: "catalog.build", Payload: string(catalogPayload), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := e.flushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reportedPaths) != 1 || reportedPaths[0] != "/api/v1/agent/jobs/events" {
		t.Fatalf("expected exactly one attempt report, got %v", reportedPaths)
	}
	if len(builtSnapshots) != 1 || builtSnapshots[0] != strings.Repeat("a", 64) {
		t.Fatalf("expected BuildCatalog called once with the catalog event's snapshot id, got %v", builtSnapshots)
	}
	remaining, err := j.UnackedOutbox(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("both rows must be acked, got %+v", remaining)
	}
}

func TestFlushOutboxSwallowsAFatalCatalogEnumerationFailureButRetriesATransientOne(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := &Executor{
		Client:  &Client{BaseURL: "http://unused.invalid"},
		Journal: j,
		BuildCatalog: func(ctx context.Context, snapshotID string) error {
			return fmt.Errorf("%w: repo auth failed", domain.ErrCatalogEnumerationFailed)
		},
	}
	fatalPayload, _ := json.Marshal(map[string]string{"snapshot_id": strings.Repeat("b", 64)})
	if err := j.AppendOutbox(context.Background(), ports.OutboxEvent{EventID: "ev-fatal", AttemptID: "att-2", Type: "catalog.build", Payload: string(fatalPayload), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.flushOutbox(context.Background()); err != nil {
		t.Fatalf("a fatal, already-reported enumeration failure must not surface as a flushOutbox error: %v", err)
	}
	remaining, err := j.UnackedOutbox(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("a fatal enumeration failure's row must still be acked (not retried forever), got %+v", remaining)
	}

	e2 := &Executor{
		Client:  &Client{BaseURL: "http://unused.invalid"},
		Journal: j,
		BuildCatalog: func(ctx context.Context, snapshotID string) error {
			return errors.New("network blip")
		},
	}
	transientPayload, _ := json.Marshal(map[string]string{"snapshot_id": strings.Repeat("c", 64)})
	if err := j.AppendOutbox(context.Background(), ports.OutboxEvent{EventID: "ev-transient", AttemptID: "att-3", Type: "catalog.build", Payload: string(transientPayload), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e2.flushOutbox(context.Background()); err == nil {
		t.Fatal("an ordinary transient failure must surface as a flushOutbox error so Poll retries later")
	}
	remaining2, err := j.UnackedOutbox(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining2) != 1 || remaining2[0].EventID != "ev-transient" {
		t.Fatalf("a transient failure's row must stay unacked for retry, got %+v", remaining2)
	}
}

func TestDispatchBuildSnapshotCatalogPropagatesAFatalEnumerationFailureAsCommandFAILED(t *testing.T) {
	e := &Executor{
		BuildCatalog: func(ctx context.Context, snapshotID string) error {
			return fmt.Errorf("%w: repo auth failed", domain.ErrCatalogEnumerationFailed)
		},
	}
	_, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "BUILD_SNAPSHOT_CATALOG", Payload: map[string]any{"snapshot_id": strings.Repeat("a", 64)}})
	if err == nil || !errors.Is(err, domain.ErrCatalogEnumerationFailed) {
		t.Fatalf("an operator-triggered BUILD_SNAPSHOT_CATALOG must surface the real error (command ends up FAILED), got %v", err)
	}
}

func TestDispatchBuildSnapshotCatalogMissingExecutorFailsClosed(t *testing.T) {
	e := &Executor{}
	if _, err := e.dispatch(context.Background(), &CommandEnvelope{Kind: "BUILD_SNAPSHOT_CATALOG", Payload: map[string]any{"snapshot_id": strings.Repeat("a", 64)}}); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("missing BuildCatalog must fail closed, got %v", err)
	}
}
