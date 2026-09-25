package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
	svcresult "github.com/canblmz1/stowline/agent/internal/windows/service"
)

// Executor runs typed control-plane commands. Unknown kinds fail closed.
type Executor struct {
	Client         *Client
	Journal        ports.Journal
	Backup         func(context.Context) error
	Restore        func(ctx context.Context, jobID, snapshot string, selections []string) error
	Canary         func(context.Context) error
	Inventory      func(context.Context) error
	Browse         func(ctx context.Context, snapshot, prefix string) (map[string]any, error)
	BrowseLocalDir func(ctx context.Context, path, cursor string, limit int) (map[string]any, error)
	BuildCatalog   func(ctx context.Context, snapshotID string) error
	ApplySelection func(ctx context.Context, revisionID string, sourceRoots, sensitiveConsents []string) (map[string]any, error)
	Cancel         func()
	OnPolicy       func(domain.LocalPolicy)
	// OnAgentPin receives the control plane's current agent_sha256/agent_qualified
	// on every successful heartbeat (see Phase 2,
	// docs/superpowers/specs/2026-09-22-ops-and-desktop-plan.md). Whether
	// that differs from the binary actually running, and whether to act on
	// it, is entirely the callback's decision -- Poll only forwards what
	// the server said.
	OnAgentPin func(sha256 string, qualified bool)
	// AgentSHA256 is this running binary's own hash, sent with every
	// heartbeat so the admin panel shows which build each PC actually runs
	// -- the only way to see an automatic upgrade (or its rollback) land.
	AgentSHA256   string
	Version       string
	PolicyRev     string
	Sequence      int
	PollSeconds   int
	PauseWAN      bool
	ProviderDown  bool
	PolicyInvalid bool
	SiteID        string
	// OnCommandEvent receives secret-free lifecycle markers for command
	// diagnostics. Production failures also surface as PollError values so
	// the service Event Log remains useful when no trace sink is attached.
	OnCommandEvent func(CommandDiagnostic)
}

// PollStage names where in the claim/dispatch/ack pipeline a Poll error
// happened, so callers can log which of them is producing failures
// without printing anything sensitive.
type PollStage string

const (
	StageHeartbeat     PollStage = "heartbeat"
	StageClaim         PollStage = "claim"
	StageConsume       PollStage = "consume"
	StageDispatch      PollStage = "dispatch"
	StageFinishCommand PollStage = "finish_command"
	StageAck           PollStage = "ack"
)

// CommandEvent is deliberately a small, stable vocabulary. These markers
// distinguish the live failure boundary without logging command payloads,
// filesystem contents, credentials, or raw errors.
type CommandEvent string

const (
	EventDispatchStarted     CommandEvent = "DISPATCH_STARTED"
	EventDispatchFinished    CommandEvent = "DISPATCH_FINISHED"
	EventJournalFinishFailed CommandEvent = "JOURNAL_FINISH_FAILED"
	EventAckStarted          CommandEvent = "ACK_STARTED"
	EventAckFailed           CommandEvent = "ACK_FAILED"
	EventAckSucceeded        CommandEvent = "ACK_SUCCEEDED"
)

type CommandDiagnostic struct {
	Event      CommandEvent
	Stage      PollStage
	CommandID  string
	Kind       string
	ErrorClass domain.ErrorClass
	// Reason is set only for a WANAdmissionDeclinedError: one of a small
	// fixed vocabulary of admission-status tokens (e.g. LOCAL_PRECHECK_PAUSED,
	// SITE_CAPACITY_WAIT, LEASE_HTTP_ERROR) -- never free text.
	Reason string
}

func (d CommandDiagnostic) String() string {
	class := d.ErrorClass
	if class == "" {
		class = "NONE"
	}
	base := fmt.Sprintf("event=%s stage=%s command_id=%s kind=%s error_class=%s",
		safeLogToken(string(d.Event)), safeLogToken(string(d.Stage)), safeLogToken(d.CommandID), safeLogToken(d.Kind), safeLogToken(string(class)))
	if d.Reason != "" {
		base += " reason=" + safeLogToken(d.Reason)
	}
	return base
}

// PollError identifies which pipeline stage failed and, for command
// stages, the command's id/kind -- never a credential or file content.
type PollError struct {
	Stage     PollStage
	CommandID string
	Kind      string
	Event     CommandEvent
	Class     domain.ErrorClass
	Err       error
}

func (e *PollError) Error() string {
	class := e.Class
	if class == "" {
		class = domain.ErrorInternal
	}
	event := e.Event
	if event == "" {
		event = CommandEvent(strings.ToUpper(string(e.Stage)) + "_FAILED")
	}
	var reason string
	var wanErr *domain.WANAdmissionDeclinedError
	if errors.As(e.Err, &wanErr) {
		reason = wanErr.Reason
	}
	// Raw errors are intentionally retained only through Unwrap. Error(),
	// which is what reaches Event Log, contains normalized non-secret fields
	// plus, for a declined WAN admission, its fixed-vocabulary Reason token.
	return CommandDiagnostic{Event: event, Stage: e.Stage, CommandID: e.CommandID, Kind: e.Kind, ErrorClass: class, Reason: reason}.String()
}

func (e *PollError) Unwrap() error { return e.Err }

func safeLogToken(value string) string {
	if value == "" {
		return "-"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 96 {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func normalizedPollClass(stage PollStage, err error) domain.ErrorClass {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrorCancelled
	}
	// winsvc.WrapBackup/WrapOperatorBackup already carry the real,
	// specific classification Service.Backup computed (e.g.
	// CONSISTENCY_NOT_MET for a VSS-inconsistent backup) inside the
	// *TickError they return. Confirmed live: a genuine VSS hiccup on an
	// operator-triggered RUN_BACKUP showed up as error_class=INTERNAL in
	// both the Event Log diagnostic and the command ack's result_json,
	// even though the durable attempt record correctly said
	// CONSISTENCY_NOT_MET -- because this function otherwise only
	// recognizes a fixed list of sentinel errors below and silently
	// falls back to INTERNAL for anything else, discarding a
	// classification that was already computed correctly.
	var tickErr *svcresult.TickError
	if errors.As(err, &tickErr) && tickErr.Class != "" {
		return tickErr.Class
	}
	for _, item := range []struct {
		target error
		class  domain.ErrorClass
	}{
		{domain.ErrBrowsePathRejected, domain.ErrorPathSafety},
		{domain.ErrSelectionRejected, domain.ErrorPathSafety},
		{domain.ErrRestorePathRejected, domain.ErrorPathSafety},
		{domain.ErrStagingEscape, domain.ErrorPathSafety},
		{domain.ErrAuth, domain.ErrorAuth},
		{domain.ErrConfig, domain.ErrorConfig},
		{domain.ErrPolicyInvalid, domain.ErrorConfig},
		{domain.ErrForbiddenCommand, domain.ErrorConfig},
		{domain.ErrWANAdmissionDeclined, domain.ErrorResourceExhausted},
	} {
		if errors.Is(err, item.target) {
			return item.class
		}
	}
	if stage == StageHeartbeat || stage == StageClaim || stage == StageAck {
		return domain.ErrorNetwork
	}
	return domain.ErrorInternal
}

func newPollError(stage PollStage, event CommandEvent, cmd *CommandEnvelope, err error) *PollError {
	pe := &PollError{Stage: stage, Event: event, Err: err, Class: normalizedPollClass(stage, err)}
	if cmd != nil {
		pe.CommandID = cmd.CommandID
		pe.Kind = cmd.Kind
	}
	return pe
}

func (e *Executor) commandEvent(event CommandEvent, stage PollStage, cmd *CommandEnvelope, class domain.ErrorClass) {
	if e.OnCommandEvent == nil || cmd == nil {
		return
	}
	e.OnCommandEvent(CommandDiagnostic{Event: event, Stage: stage, CommandID: cmd.CommandID, Kind: cmd.Kind, ErrorClass: class})
}

func (e *Executor) Poll(ctx context.Context, currentOp string) error {
	if e.Client == nil || e.Client.BaseURL == "" {
		return nil
	}
	if err := e.flushOutbox(ctx); err != nil {
		return newPollError(StageHeartbeat, "OUTBOX_FLUSH_FAILED", nil, err)
	}
	e.Sequence++
	hb, err := e.Client.Heartbeat(ctx, e.Sequence, e.Version, currentOp, e.PolicyRev, e.AgentSHA256)
	if err != nil {
		return newPollError(StageHeartbeat, "HEARTBEAT_FAILED", nil, err)
	}
	if hb != nil {
		if hb.PollSeconds > 0 {
			e.PollSeconds = hb.PollSeconds
		}
		e.PauseWAN = hb.PauseNewWANAdmissions
		e.ProviderDown = hb.ProviderUnavailable
		e.SiteID = hb.SiteID
		if err := e.acceptPolicy(ctx, hb.DesiredPolicyRevisionID, hb.Policy); err != nil {
			e.PolicyInvalid = true
			return newPollError(StageHeartbeat, "HEARTBEAT_POLICY_FAILED", nil, err)
		}
		e.PolicyInvalid = false
		if e.OnAgentPin != nil {
			e.OnAgentPin(hb.AgentSHA256, hb.AgentQualified)
		}
	}
	cmd, err := e.Client.Claim(ctx)
	if err != nil {
		return newPollError(StageClaim, "CLAIM_FAILED", cmd, err)
	}
	if cmd == nil {
		return nil
	}
	if e.Journal == nil {
		return newPollError(StageConsume, "CONSUME_FAILED", cmd, fmt.Errorf("%w: command journal missing", domain.ErrConfig))
	}
	already, err := e.Journal.ConsumeCommand(ctx, cmd.CommandID, cmd.JobID, false)
	if err != nil {
		return newPollError(StageConsume, "CONSUME_FAILED", cmd, err)
	}
	if already {
		terminal, recorded, extra, err := e.Journal.CommandResult(ctx, cmd.CommandID)
		if err != nil {
			return newPollError(StageConsume, "CONSUME_FAILED", cmd, err)
		}
		if terminal {
			// Legacy rows could be terminal without a recorded state. Persist
			// FAILED before replay; never invent a successful result.
			if recorded == "" {
				recorded = "FAILED"
				if err := e.Journal.FinishCommand(ctx, cmd.CommandID, recorded, extra); err != nil {
					e.commandEvent(EventJournalFinishFailed, StageFinishCommand, cmd, normalizedPollClass(StageFinishCommand, err))
					return newPollError(StageFinishCommand, EventJournalFinishFailed, cmd, err)
				}
			}
			e.commandEvent(EventAckStarted, StageAck, cmd, "")
			if ackErr := e.Client.AckResult(ctx, cmd.CommandID, recorded, extra); ackErr != nil {
				class := normalizedPollClass(StageAck, ackErr)
				e.commandEvent(EventAckFailed, StageAck, cmd, class)
				return newPollError(StageAck, EventAckFailed, cmd, ackErr)
			}
			e.commandEvent(EventAckSucceeded, StageAck, cmd, "")
			return nil
		}
		// Non-terminal means the process stopped between consume and durable
		// finish. Re-dispatch under the handlers' idempotency rules.
	}
	e.commandEvent(EventDispatchStarted, StageDispatch, cmd, "")
	extra, runErr := e.dispatch(ctx, cmd)
	runClass := normalizedPollClass(StageDispatch, runErr)
	if runErr == nil {
		runClass = ""
	}
	e.commandEvent(EventDispatchFinished, StageDispatch, cmd, runClass)
	state := "SUCCEEDED"
	if runErr != nil {
		state = "FAILED"
	}
	if err := e.Journal.FinishCommand(ctx, cmd.CommandID, state, extra); err != nil {
		class := normalizedPollClass(StageFinishCommand, err)
		e.commandEvent(EventJournalFinishFailed, StageFinishCommand, cmd, class)
		return newPollError(StageFinishCommand, EventJournalFinishFailed, cmd, err)
	}
	e.commandEvent(EventAckStarted, StageAck, cmd, "")
	if ackErr := e.Client.AckResult(ctx, cmd.CommandID, state, extra); ackErr != nil {
		class := normalizedPollClass(StageAck, ackErr)
		e.commandEvent(EventAckFailed, StageAck, cmd, class)
		return newPollError(StageAck, EventAckFailed, cmd, ackErr)
	}
	e.commandEvent(EventAckSucceeded, StageAck, cmd, "")
	if runErr != nil {
		return newPollError(StageDispatch, EventDispatchFinished, cmd, runErr)
	}
	return nil
}

func (e *Executor) flushOutbox(ctx context.Context) error {
	if e.Journal == nil {
		return nil
	}
	rows, err := e.Journal.UnackedOutbox(ctx, 32)
	if err != nil {
		return err
	}
	for _, ev := range rows {
		switch ev.Type {
		case "catalog.build":
			if err := e.flushCatalogEvent(ctx, ev); err != nil {
				return err
			}
		default:
			payload := outboxToAttempt(ev.Payload, string(ev.AttemptID), ev.CreatedAt)
			if err := e.Client.ReportAttempt(ctx, payload); err != nil {
				return err
			}
		}
		if err := e.Journal.AckOutbox(ctx, ev.EventID); err != nil {
			return err
		}
	}
	return nil
}

// flushCatalogEvent runs the catalog build+upload for one durably-queued
// snapshot. BuildCatalog itself never swallows anything -- it always calls
// FailCatalog and returns a real error on a fatal enumeration failure,
// because the *other* caller (the BUILD_SNAPSHOT_CATALOG command dispatch
// in dispatch() below) needs that error to report FAILED to the operator.
// This automatic path is the one place that must NOT propagate that same
// error: a permanently broken snapshot's outbox row would otherwise retry
// forever and block every other outbox row queued after it.
// domain.ErrCatalogEnumerationFailed is the shared marker that lets this
// function -- and only this function -- make that call.
func (e *Executor) flushCatalogEvent(ctx context.Context, ev ports.OutboxEvent) error {
	if e.BuildCatalog == nil {
		return nil // no catalog support wired -- ack and move on rather than blocking every other outbox row forever
	}
	var payload struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil || payload.SnapshotID == "" {
		return nil // malformed payload can never succeed -- ack rather than retry forever
	}
	err := e.BuildCatalog(ctx, payload.SnapshotID)
	if err != nil && errors.Is(err, domain.ErrCatalogEnumerationFailed) {
		return nil
	}
	return err
}

func outboxToAttempt(raw, attemptID string, createdAt time.Time) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	get := func(names ...string) any {
		for _, n := range names {
			if v, ok := m[n]; ok && v != nil && v != "" {
				return v
			}
		}
		return ""
	}
	out := map[string]any{
		"attempt_id":          attemptID,
		"outcome":             strings.ToUpper(fmt.Sprint(get("Outcome", "outcome"))),
		"snapshot_id":         fmt.Sprint(get("SnapshotID", "snapshot_id")),
		"error_class":         fmt.Sprint(get("ErrorClass", "error_class")),
		"published_not_green": get("PublishedNotGreen", "published_not_green"),
		"consistency":         fmt.Sprint(get("Consistency", "consistency")),
		"job_id":              fmt.Sprint(get("JobID", "job_id")),
	}
	// The outbox's own CreatedAt is this attempt's real local completion
	// time, recorded in the journal when it actually happened -- unlike the
	// server's receive time, it stays correct even when delivery is
	// delayed (retried outage, or a server-side bug blocking ingestion for
	// a while). The server uses it to refuse to move a device's
	// last-success state backward when an older report arrives late.
	if !createdAt.IsZero() {
		out["attempt_ended_at"] = createdAt.UTC().Format(time.RFC3339Nano)
	}
	if sum, ok := m["Summary"].(map[string]any); ok {
		out["bytes_added"] = sum["DataAdded"]
		out["files_new"] = sum["FilesNew"]
	}
	return out
}

func (e *Executor) acceptPolicy(ctx context.Context, revisionID string, cfg map[string]any) error {
	if revisionID == "" || cfg == nil {
		return nil
	}
	p, err := PolicyFromServer(revisionID, cfg)
	if err != nil {
		return fmt.Errorf("%w: rejecting malformed server policy", err)
	}
	if e.Journal != nil {
		latest, _ := e.Journal.LatestPolicy(ctx)
		changed := latest == nil || latest.RevisionID != p.RevisionID || latest.BandwidthKiBps != p.BandwidthKiBps || latest.RestoreDownloadKiBps != p.RestoreDownloadKiBps
		if changed {
			if err := e.Journal.SavePolicy(ctx, p); err != nil {
				return err
			}
		}
	}
	e.PolicyRev = p.RevisionID
	if e.OnPolicy != nil {
		e.OnPolicy(p)
	}
	return nil
}

func (e *Executor) dispatch(ctx context.Context, cmd *CommandEnvelope) (map[string]any, error) {
	switch cmd.Kind {
	case "REPORT_INVENTORY":
		if e.Inventory != nil {
			return nil, e.Inventory(ctx)
		}
		return nil, nil
	case "REFRESH_POLICY":
		if e.Client == nil {
			return nil, fmt.Errorf("%w: policy client missing", domain.ErrConfig)
		}
		pr, err := e.Client.Policy(ctx)
		if err != nil {
			return nil, err
		}
		if pr == nil {
			return nil, fmt.Errorf("%w: empty policy", domain.ErrPolicyInvalid)
		}
		if err := e.acceptPolicy(ctx, pr.RevisionID, pr.Config); err != nil {
			return nil, err
		}
		return map[string]any{"revision_id": pr.RevisionID}, nil
	case "RUN_BACKUP":
		if e.Backup == nil {
			return nil, fmt.Errorf("%w: backup executor missing", domain.ErrConfig)
		}
		runErr := e.Backup(ctx)
		var wanErr *domain.WANAdmissionDeclinedError
		if errors.As(runErr, &wanErr) {
			return map[string]any{"error_class": "RESOURCE_EXHAUSTED", "reason": wanErr.Reason}, runErr
		}
		// Anything else non-nil is a *winsvc.TickError (WrapOperatorBackup
		// only returns nil on success): surface its already-computed
		// Class (e.g. CONSISTENCY_NOT_MET for a VSS-inconsistent backup,
		// confirmed live) so the operator's ack shows the real reason
		// instead of an empty extra that leaves only a generic INTERNAL
		// in the diagnostic log.
		var tickErr *svcresult.TickError
		if errors.As(runErr, &tickErr) && tickErr.Class != "" {
			return map[string]any{"error_class": string(tickErr.Class)}, runErr
		}
		return nil, runErr
	case "RUN_CANARY":
		if e.Canary == nil {
			return nil, fmt.Errorf("%w: canary executor missing", domain.ErrConfig)
		}
		return nil, e.Canary(ctx)
	case "BROWSE_SNAPSHOT":
		snap, _ := cmd.Payload["snapshot_id"].(string)
		prefix, _ := cmd.Payload["prefix"].(string)
		if e.Browse == nil {
			return nil, fmt.Errorf("%w: browse executor missing", domain.ErrConfig)
		}
		return e.Browse(ctx, snap, prefix)
	case "BUILD_SNAPSHOT_CATALOG":
		snap, _ := cmd.Payload["snapshot_id"].(string)
		if e.BuildCatalog == nil {
			return nil, fmt.Errorf("%w: catalog executor missing", domain.ErrConfig)
		}
		// Deliberately does not check for domain.ErrCatalogEnumerationFailed
		// the way flushCatalogEvent above does -- an operator-triggered
		// BUILD_SNAPSHOT_CATALOG command must report FAILED on that error
		// (SUCCEEDED once finalize returns 200, FAILED otherwise), not
		// silently succeed just because the automatic path would swallow it.
		return nil, e.BuildCatalog(ctx, snap)
	case "RESTORE_TO_STAGING":
		snap, _ := cmd.Payload["snapshot_id"].(string)
		mode, _ := cmd.Payload["destination_mode"].(string)
		if mode != "" && mode != "STAGING" {
			return nil, fmt.Errorf("%w: only staged restore is allowed", domain.ErrRestorePathRejected)
		}
		var sel []string
		if raw, ok := cmd.Payload["selections"].([]any); ok {
			for _, x := range raw {
				s, _ := x.(string)
				if strings.Contains(s, "..") {
					return nil, domain.ErrRestorePathRejected
				}
				sel = append(sel, s)
			}
		}
		if e.Restore == nil {
			return nil, fmt.Errorf("%w: restore executor missing", domain.ErrConfig)
		}
		return nil, e.Restore(ctx, cmd.JobID, snap, sel)
	case "BROWSE_LOCAL_DIR":
		path, _ := cmd.Payload["path"].(string)
		cursor, _ := cmd.Payload["cursor"].(string)
		limit := 200
		if raw, ok := cmd.Payload["limit"].(float64); ok && raw > 0 {
			limit = int(raw)
		}
		if e.BrowseLocalDir == nil {
			return nil, fmt.Errorf("%w: local browse executor missing", domain.ErrConfig)
		}
		return e.BrowseLocalDir(ctx, path, cursor, limit)
	case "APPLY_SELECTION":
		revisionID, _ := cmd.Payload["revision_id"].(string)
		var roots, consents []string
		if raw, ok := cmd.Payload["source_roots"].([]any); ok {
			for _, x := range raw {
				s, _ := x.(string)
				roots = append(roots, s)
			}
		}
		if raw, ok := cmd.Payload["sensitive_consents"].([]any); ok {
			for _, x := range raw {
				s, _ := x.(string)
				consents = append(consents, s)
			}
		}
		if e.ApplySelection == nil {
			return nil, fmt.Errorf("%w: apply-selection executor missing", domain.ErrConfig)
		}
		return e.ApplySelection(ctx, revisionID, roots, consents)
	case "CANCEL_CURRENT_SAFE_OPERATION":
		if e.Cancel != nil {
			e.Cancel()
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: %s", domain.ErrForbiddenCommand, cmd.Kind)
	}
}
