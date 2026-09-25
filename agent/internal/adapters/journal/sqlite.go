package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const schema = `
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  slot_key TEXT UNIQUE,
  policy_revision_id TEXT,
  state TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attempts (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  attempt_no INTEGER NOT NULL,
  kind TEXT NOT NULL,
  slot_key TEXT,
  policy_revision_id TEXT,
  phase TEXT NOT NULL,
  outcome TEXT,
  snapshot_id TEXT,
  error_class TEXT,
  summary_json TEXT,
  evidence_json TEXT,
  intent_at TEXT NOT NULL,
  started_at TEXT,
  ended_at TEXT,
  UNIQUE(job_id, attempt_no),
  FOREIGN KEY(job_id) REFERENCES jobs(id)
);
CREATE TABLE IF NOT EXISTS outbox (
  event_id TEXT PRIMARY KEY,
  attempt_id TEXT,
  seq INTEGER,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  acked_at TEXT
);
CREATE TABLE IF NOT EXISTS consumed_commands (
  command_id TEXT PRIMARY KEY,
  job_id TEXT,
  consumed_at TEXT NOT NULL,
  terminal INTEGER NOT NULL,
  result TEXT NOT NULL DEFAULT '',
  extra_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS policy_revisions (
  id TEXT PRIMARY KEY,
  revision_json TEXT NOT NULL,
  accepted_at TEXT NOT NULL
);
`

type SQLite struct {
	path string
	db   *sql.DB
}

func Open(path string) (*SQLite, error) {
	j := &SQLite{path: path}
	if err := j.Open(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *SQLite) Open() error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)", j.path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = db.Close()
		return err
	}
	if err := migrateJobs(db); err != nil {
		_ = db.Close()
		return err
	}
	j.db = db
	return nil
}

func migrateJobs(db *sql.DB) error {
	for _, ddl := range []string{
		`ALTER TABLE jobs ADD COLUMN request_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE jobs ADD COLUMN request_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE consumed_commands ADD COLUMN result TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE consumed_commands ADD COLUMN extra_json TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			msg := err.Error()
			if !(containsFold(msg, "duplicate column") || containsFold(msg, "already exists")) {
				return err
			}
		}
	}
	_, _ = db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (2, ?)`, time.Now().UTC().Format(time.RFC3339Nano))
	return nil
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func (j *SQLite) Close() error {
	if j.db != nil {
		return j.db.Close()
	}
	return nil
}

func (j *SQLite) BeginJob(ctx context.Context, job ports.JobRecord, attempt ports.AttemptRecord) error {
	if j.db == nil {
		return domain.ErrIntentNotPersisted
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrIntentNotPersisted, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.UpdatedAt = time.Now().UTC()
	attempt.IntentAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id, kind, slot_key, policy_revision_id, state, created_at, updated_at, request_hash, request_json)
		VALUES (?,?,?,?,?,?,?,?,?)`, job.ID, job.Kind, nullStr(job.SlotKey), job.PolicyRevisionID, job.State, job.CreatedAt.Format(time.RFC3339Nano), job.UpdatedAt.Format(time.RFC3339Nano), job.RequestHash, job.RequestJSON)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: %v", domain.ErrIntentNotPersisted, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempts(id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		attempt.ID, job.ID, attempt.AttemptNo, attempt.Kind, nullStr(attempt.SlotKey), attempt.PolicyRevisionID,
		attempt.Phase, nullStr(string(attempt.Outcome)), nullStr(string(attempt.SnapshotID)), string(attempt.ErrorClass),
		attempt.SummaryJSON, attempt.EvidenceJSON, attempt.IntentAt.Format(time.RFC3339Nano), nullTime(attempt.StartedAt), nullTime(attempt.EndedAt))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: %v", domain.ErrIntentNotPersisted, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrIntentNotPersisted, err)
	}
	_ = now
	return nil
}

func (j *SQLite) UpdateAttempt(ctx context.Context, rec ports.AttemptRecord) error {
	_, err := j.db.ExecContext(ctx, `UPDATE attempts SET phase=?, outcome=?, snapshot_id=?, error_class=?, summary_json=?, evidence_json=?, started_at=?, ended_at=? WHERE id=?`,
		rec.Phase, nullStr(string(rec.Outcome)), nullStr(string(rec.SnapshotID)), string(rec.ErrorClass), rec.SummaryJSON, rec.EvidenceJSON, nullTime(rec.StartedAt), nullTime(rec.EndedAt), rec.ID)
	return err
}

func (j *SQLite) CompleteAttempt(ctx context.Context, rec ports.AttemptRecord, jobState domain.JobState) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if rec.EndedAt.IsZero() {
		rec.EndedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET phase=?, outcome=?, snapshot_id=?, error_class=?, summary_json=?, evidence_json=?, ended_at=? WHERE id=?`,
		rec.Phase, string(rec.Outcome), nullStr(string(rec.SnapshotID)), string(rec.ErrorClass), rec.SummaryJSON, rec.EvidenceJSON, rec.EndedAt.Format(time.RFC3339Nano), rec.ID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state=?, updated_at=? WHERE id=?`, jobState, time.Now().UTC().Format(time.RFC3339Nano), rec.JobID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (j *SQLite) GetAttempt(ctx context.Context, id domain.AttemptID) (*ports.AttemptRecord, error) {
	row := j.db.QueryRowContext(ctx, `SELECT id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at FROM attempts WHERE id=?`, id)
	return scanAttempt(row)
}

func (j *SQLite) GetJob(ctx context.Context, id domain.JobID) (*ports.JobRecord, error) {
	row := j.db.QueryRowContext(ctx, `SELECT id, kind, slot_key, policy_revision_id, state, created_at, updated_at, request_hash, request_json FROM jobs WHERE id=?`, id)
	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return job, err
}

func (j *SQLite) JobBySlot(ctx context.Context, slotKey string) (*ports.JobRecord, error) {
	row := j.db.QueryRowContext(ctx, `SELECT id, kind, slot_key, policy_revision_id, state, created_at, updated_at, request_hash, request_json FROM jobs WHERE slot_key=?`, slotKey)
	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return job, err
}

func (j *SQLite) IncompleteAttempts(ctx context.Context) ([]ports.AttemptRecord, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at FROM attempts WHERE phase NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.AttemptRecord
	for rows.Next() {
		rec, err := scanAttemptRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func (j *SQLite) ConsumeCommand(ctx context.Context, commandID, jobID string, terminal bool) (bool, error) {
	var existing string
	err := j.db.QueryRowContext(ctx, `SELECT command_id FROM consumed_commands WHERE command_id=?`, commandID).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	term := 0
	if terminal {
		term = 1
	}
	_, err = j.db.ExecContext(ctx, `INSERT INTO consumed_commands(command_id, job_id, consumed_at, terminal) VALUES (?,?,?,?)`, commandID, jobID, time.Now().UTC().Format(time.RFC3339Nano), term)
	return false, err
}

func (j *SQLite) CommandConsumed(ctx context.Context, commandID string) (bool, error) {
	var n int
	err := j.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM consumed_commands WHERE command_id=?`, commandID).Scan(&n)
	return n > 0, err
}

func (j *SQLite) CommandResult(ctx context.Context, commandID string) (bool, string, map[string]any, error) {
	var terminal int
	var state, extraRaw string
	err := j.db.QueryRowContext(ctx, `SELECT terminal, COALESCE(result,''), COALESCE(extra_json,'') FROM consumed_commands WHERE command_id=?`, commandID).Scan(&terminal, &state, &extraRaw)
	if err == sql.ErrNoRows {
		return false, "", nil, nil
	}
	if err != nil {
		return false, "", nil, err
	}
	var extra map[string]any
	if extraRaw != "" {
		if err := json.Unmarshal([]byte(extraRaw), &extra); err != nil {
			// A corrupt durable payload must never be silently replayed as an
			// empty result: BROWSE_LOCAL_DIR and APPLY_SELECTION depend on the
			// exact stored JSON reaching the control plane.
			return false, "", nil, fmt.Errorf("decode command result: %w", err)
		}
	}
	return terminal != 0, state, extra, nil
}

func (j *SQLite) FinishCommand(ctx context.Context, commandID, state string, extra map[string]any) error {
	extraRaw := ""
	if extra != nil {
		b, err := json.Marshal(extra)
		if err != nil {
			return err
		}
		extraRaw = string(b)
	}
	res, err := j.db.ExecContext(ctx, `UPDATE consumed_commands SET terminal=1, result=?, extra_json=? WHERE command_id=?`, state, extraRaw, commandID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		_, err = j.db.ExecContext(ctx, `INSERT INTO consumed_commands(command_id, job_id, consumed_at, terminal, result, extra_json) VALUES (?,?,?,?,?,?)`,
			commandID, "", time.Now().UTC().Format(time.RFC3339Nano), 1, state, extraRaw)
	}
	return err
}

func (j *SQLite) AppendOutbox(ctx context.Context, ev ports.OutboxEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	_, err := j.db.ExecContext(ctx, `INSERT INTO outbox(event_id, attempt_id, seq, type, payload, created_at) VALUES (?,?,?,?,?,?)`,
		ev.EventID, ev.AttemptID, ev.Seq, ev.Type, ev.Payload, ev.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (j *SQLite) UnackedOutbox(ctx context.Context, limit int) ([]ports.OutboxEvent, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT event_id, attempt_id, seq, type, payload, created_at, acked_at FROM outbox WHERE acked_at IS NULL OR acked_at='' ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.OutboxEvent
	for rows.Next() {
		var ev ports.OutboxEvent
		var attempt, acked sql.NullString
		var created string
		if err := rows.Scan(&ev.EventID, &attempt, &ev.Seq, &ev.Type, &ev.Payload, &created, &acked); err != nil {
			return nil, err
		}
		ev.AttemptID = domain.AttemptID(attempt.String)
		ev.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if acked.Valid {
			ev.AckedAt, _ = time.Parse(time.RFC3339Nano, acked.String)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (j *SQLite) AckOutbox(ctx context.Context, eventID string) error {
	_, err := j.db.ExecContext(ctx, `UPDATE outbox SET acked_at=? WHERE event_id=?`, time.Now().UTC().Format(time.RFC3339Nano), eventID)
	return err
}

func (j *SQLite) SavePolicy(ctx context.Context, p domain.LocalPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `INSERT OR REPLACE INTO policy_revisions(id, revision_json, accepted_at) VALUES (?,?,?)`, p.RevisionID, string(b), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (j *SQLite) LatestSucceededBackupSlot(ctx context.Context) (string, error) {
	var slot sql.NullString
	err := j.db.QueryRowContext(ctx, `SELECT slot_key FROM jobs WHERE kind=? AND state='SUCCEEDED' AND slot_key IS NOT NULL AND slot_key != '' AND slot_key NOT LIKE 'qualification|%' ORDER BY updated_at DESC LIMIT 1`, domain.JobBackup).Scan(&slot)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return slot.String, nil
}

func (j *SQLite) LatestAttempt(ctx context.Context, jobID domain.JobID) (*ports.AttemptRecord, error) {
	row := j.db.QueryRowContext(ctx, `SELECT id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at FROM attempts WHERE job_id=? ORDER BY attempt_no DESC LIMIT 1`, jobID)
	rec, err := scanAttempt(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

func (j *SQLite) RecentAttempts(ctx context.Context, limit int) ([]ports.AttemptRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := j.db.QueryContext(ctx, `SELECT id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at FROM attempts ORDER BY COALESCE(ended_at, started_at, intent_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.AttemptRecord
	for rows.Next() {
		rec, err := scanAttemptRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func (j *SQLite) BeginAttempt(ctx context.Context, attempt ports.AttemptRecord) error {
	if j.db == nil {
		return domain.ErrIntentNotPersisted
	}
	if attempt.IntentAt.IsZero() {
		attempt.IntentAt = time.Now().UTC()
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO attempts(id, job_id, attempt_no, kind, slot_key, policy_revision_id, phase, outcome, snapshot_id, error_class, summary_json, evidence_json, intent_at, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		attempt.ID, attempt.JobID, attempt.AttemptNo, attempt.Kind, nullStr(attempt.SlotKey), attempt.PolicyRevisionID,
		attempt.Phase, nullStr(string(attempt.Outcome)), nullStr(string(attempt.SnapshotID)), string(attempt.ErrorClass),
		attempt.SummaryJSON, attempt.EvidenceJSON, attempt.IntentAt.Format(time.RFC3339Nano), nullTime(attempt.StartedAt), nullTime(attempt.EndedAt))
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrIntentNotPersisted, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state=?, updated_at=? WHERE id=?`, domain.JobActive, time.Now().UTC().Format(time.RFC3339Nano), attempt.JobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *SQLite) SetJobState(ctx context.Context, id domain.JobID, state domain.JobState) error {
	_, err := j.db.ExecContext(ctx, `UPDATE jobs SET state=?, updated_at=? WHERE id=?`, state, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (j *SQLite) LatestPolicy(ctx context.Context) (*domain.LocalPolicy, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT revision_json FROM policy_revisions ORDER BY accepted_at DESC LIMIT 1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p domain.LocalPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (*ports.JobRecord, error) {
	var job ports.JobRecord
	var slot sql.NullString
	var created, updated, hash, payload string
	if err := row.Scan(&job.ID, &job.Kind, &slot, &job.PolicyRevisionID, &job.State, &created, &updated, &hash, &payload); err != nil {
		return nil, err
	}
	job.SlotKey = slot.String
	job.RequestHash = hash
	job.RequestJSON = payload
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &job, nil
}

func scanAttempt(row scanner) (*ports.AttemptRecord, error) {
	var rec ports.AttemptRecord
	var slot, outcome, snap, intent, started, ended sql.NullString
	if err := row.Scan(&rec.ID, &rec.JobID, &rec.AttemptNo, &rec.Kind, &slot, &rec.PolicyRevisionID, &rec.Phase, &outcome, &snap, &rec.ErrorClass, &rec.SummaryJSON, &rec.EvidenceJSON, &intent, &started, &ended); err != nil {
		return nil, err
	}
	rec.SlotKey = slot.String
	rec.Outcome = domain.AttemptPhase(outcome.String)
	rec.SnapshotID = domain.SnapshotID(snap.String)
	rec.IntentAt, _ = time.Parse(time.RFC3339Nano, intent.String)
	if started.Valid {
		rec.StartedAt, _ = time.Parse(time.RFC3339Nano, started.String)
	}
	if ended.Valid {
		rec.EndedAt, _ = time.Parse(time.RFC3339Nano, ended.String)
	}
	return &rec, nil
}

func scanAttemptRows(rows *sql.Rows) (*ports.AttemptRecord, error) {
	return scanAttempt(rows)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
