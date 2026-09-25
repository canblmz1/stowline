package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const ProtocolSchema = 1

var allowedKinds = map[string]struct{}{
	"RUN_BACKUP": {}, "RESTORE_TO_STAGING": {}, "RUN_CANARY": {},
	"REFRESH_POLICY": {}, "REPORT_INVENTORY": {}, "CANCEL_CURRENT_SAFE_OPERATION": {},
	"BROWSE_SNAPSHOT": {}, "BROWSE_LOCAL_DIR": {}, "APPLY_SELECTION": {},
	"BUILD_SNAPSHOT_CATALOG": {},
}

type Client struct {
	BaseURL    string
	Credential string
	HTTP       *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) Enroll(ctx context.Context, token, hostname, version, installationID string) (*EnrollResult, error) {
	body := map[string]any{
		"token": token, "hostname": hostname, "agent_version": version,
		"installation_id": installationID, "capabilities": map[string]any{"vss": true, "restic": "0.19.1"},
	}
	var out EnrollResult
	if err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/enrollments", "", body, &out); err != nil {
		return nil, err
	}
	if out.ControlCredential == "" || out.DeviceID == "" {
		return nil, fmt.Errorf("%w: incomplete enrollment", domain.ErrConfig)
	}
	return &out, nil
}

// AbortEnrollment quarantines a just-created device using its one-time control bearer.
func (c *Client) AbortEnrollment(ctx context.Context, controlCredential string) error {
	if strings.TrimSpace(controlCredential) == "" {
		return fmt.Errorf("%w: abort requires control credential", domain.ErrConfig)
	}
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/enrollments/abort", controlCredential, map[string]any{"reason": "local_persistence_failed"}, nil)
}

func (c *Client) Heartbeat(ctx context.Context, seq int, version, op, policyRev, agentSHA256 string) (*HeartbeatResult, error) {
	var out HeartbeatResult
	body := map[string]any{
		"sequence": seq, "agent_version": version, "current_operation": op, "policy_revision_id": policyRev,
	}
	if agentSHA256 != "" {
		body["agent_sha256"] = agentSHA256
	}
	err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/heartbeat", c.Credential, body, &out)
	return &out, err
}

func (c *Client) AcquireLease(ctx context.Context, body map[string]any) (*AdmissionResult, error) {
	var out AdmissionResult
	err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/wan/leases/acquire", c.Credential, body, &out)
	return &out, err
}

func (c *Client) RenewLease(ctx context.Context, leaseID string, progress *domain.BackupProgress) (*AdmissionResult, error) {
	var out AdmissionResult
	body := map[string]any{}
	if progress != nil {
		body = map[string]any{
			"percent_done": progress.PercentDone,
			"files_done":   progress.FilesDone,
			"total_files":  progress.TotalFiles,
			"bytes_done":   progress.BytesDone,
			"total_bytes":  progress.TotalBytes,
		}
	}
	err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/wan/leases/"+url.PathEscape(leaseID)+"/renew", c.Credential, body, &out)
	return &out, err
}

func (c *Client) ReleaseLease(ctx context.Context, leaseID, reason string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/wan/leases/"+url.PathEscape(leaseID)+"/release", c.Credential, map[string]any{"reason": reason}, nil)
}

func (c *Client) Policy(ctx context.Context) (*PolicyResult, error) {
	var out PolicyResult
	err := c.roundTrip(ctx, http.MethodGet, "/api/v1/agent/policy", c.Credential, nil, &out)
	return &out, err
}

func (c *Client) Claim(ctx context.Context) (*CommandEnvelope, error) {
	var wrap struct {
		Command *CommandEnvelope `json:"command"`
	}
	if err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/work/claim", c.Credential, map[string]any{}, &wrap); err != nil {
		return nil, err
	}
	if wrap.Command == nil {
		return nil, nil
	}
	if _, ok := allowedKinds[wrap.Command.Kind]; !ok {
		// Return the envelope alongside the validation error so Poll can log
		// the server-issued command id/kind while still refusing dispatch.
		return wrap.Command, fmt.Errorf("%w: remote command %s", domain.ErrForbiddenCommand, wrap.Command.Kind)
	}
	if wrap.Command.Kind == "EXEC" || strings.EqualFold(wrap.Command.Kind, "shell") {
		return wrap.Command, domain.ErrForbiddenCommand
	}
	return wrap.Command, nil
}

func (c *Client) ReportAttempt(ctx context.Context, payload map[string]any) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/jobs/events", c.Credential, payload, nil)
}

func (c *Client) CreateCatalog(ctx context.Context, snapshotID string, declared map[string]any) (string, string, error) {
	body := map[string]any{"snapshot_id": snapshotID}
	for k, v := range declared {
		body[k] = v
	}
	var out struct {
		CatalogID string `json:"catalog_id"`
		State     string `json:"state"`
	}
	err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/snapshot-catalogs", c.Credential, body, &out)
	return out.CatalogID, out.State, err
}

func (c *Client) UploadCatalogEntries(ctx context.Context, catalogID string, entries []map[string]any) error {
	body := map[string]any{"entries": entries}
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/snapshot-catalogs/"+url.PathEscape(catalogID)+"/entries", c.Credential, body, nil)
}

func (c *Client) FinalizeCatalog(ctx context.Context, catalogID string, declared map[string]any) (bool, error) {
	var out struct {
		OK bool `json:"ok"`
	}
	err := c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/snapshot-catalogs/"+url.PathEscape(catalogID)+"/finalize", c.Credential, declared, &out)
	return out.OK, err
}

func (c *Client) FailCatalog(ctx context.Context, catalogID, errorClass string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/snapshot-catalogs/"+url.PathEscape(catalogID)+"/fail", c.Credential, map[string]any{"error_class": errorClass}, nil)
}

// PutEscrow seals a copy of the agent's own repository password into the
// control plane's vault (server/app/vault.py) so an admin can still reach
// it via Şifre Kasası if this machine is later lost -- server-side storage
// is already sealed/audited/throttled; this call only supplies the secret.
func (c *Client) PutEscrow(ctx context.Context, kind, secret string) error {
	return c.roundTrip(ctx, http.MethodPut, "/api/v1/agent/escrow", c.Credential, map[string]any{"kind": kind, "secret": secret}, nil)
}

// ReportSelfServiceSelection tells the control plane the user changed the
// backed-up folders from the local desktop page, so the admin panel keeps
// showing what this PC actually backs up.
func (c *Client) ReportSelfServiceSelection(ctx context.Context, sourceRoots []string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/self-service-selection", c.Credential, map[string]any{"source_roots": sourceRoots}, nil)
}

// SyncSourceRoots reports the folders pilot.json says this PC backs up,
// tagged so the server can tell a start-up sync from a user's change and
// never let it override an admin selection the PC hasn't applied yet.
func (c *Client) SyncSourceRoots(ctx context.Context, sourceRoots []string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/self-service-selection", c.Credential, map[string]any{"source_roots": sourceRoots, "source": "device_sync"}, nil)
}

// RequestSelfServiceBackup asks the control plane to queue a RUN_BACKUP for
// this device: the "Şimdi yedekle" button in Stowline Backups then runs the
// exact same path as an admin's click.
func (c *Client) RequestSelfServiceBackup(ctx context.Context) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/self-service-backup", c.Credential, map[string]any{}, nil)
}

// SendUserMessage delivers the user's "IT'ye haber ver" text to Olaylar.
func (c *Client) SendUserMessage(ctx context.Context, message string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/user-messages", c.Credential, map[string]any{"message": message}, nil)
}

// ReportSelfServiceRestore tells the control plane that the end user
// restored something themselves from the local desktop client -- audit
// only (server/app/services.py's report_self_service_restore), never a
// gate: the restore already happened locally by the time this is called.
func (c *Client) ReportSelfServiceRestore(ctx context.Context, snapshotID string, selections []string, destination string) error {
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/self-service-restores", c.Credential, map[string]any{
		"snapshot_id": snapshotID, "selections": selections, "destination": destination,
	}, nil)
}

// maxAgentBinaryBytes bounds a downloaded agent build. A real stowline-agent.exe
// is a few tens of MB; this exists only so a misbehaving or compromised
// server response can't exhaust memory on the endpoint.
const maxAgentBinaryBytes = 256 << 20

// DownloadAgentBinary fetches the currently qualified agent build for the
// automatic-upgrade flow. It never trusts the bytes on its own -- the
// caller (see cmd/stowline-agent's upgrade logic) must still verify the
// SHA-256 against the pin from Heartbeat before treating this as anything
// but untrusted downloaded data.
func (c *Client) DownloadAgentBinary(ctx context.Context) ([]byte, error) {
	if err := validateBase(c.BaseURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/api/v1/agent/binary", nil)
	if err != nil {
		return nil, err
	}
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxAgentBinaryBytes))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("control plane %s: %s", res.Status, domain.Redact(string(raw)))
	}
	return raw, nil
}

func (c *Client) Ack(ctx context.Context, commandID, state string) error {
	return c.AckResult(ctx, commandID, state, nil)
}

func (c *Client) AckResult(ctx context.Context, commandID, state string, extra map[string]any) error {
	body := map[string]any{"state": state}
	if extra != nil {
		body["extra"] = extra
	}
	return c.roundTrip(ctx, http.MethodPost, "/api/v1/agent/commands/"+url.PathEscape(commandID)+"/ack", c.Credential, body, nil)
}

func (c *Client) FlushOutbox(ctx context.Context, events []map[string]any) error {
	for _, payload := range events {
		if err := c.ReportAttempt(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) roundTrip(ctx context.Context, method, path, bearer string, body any, out any) error {
	if err := validateBase(c.BaseURL); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		return fmt.Errorf("control plane %s: %s", res.Status, domain.Redact(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func validateBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: control plane URL", domain.ErrConfig)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials must not appear in control-plane URLs", domain.ErrConfig)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%w: control plane URL scheme", domain.ErrConfig)
	}
	return nil
}

type EnrollResult struct {
	DeviceID          string         `json:"device_id"`
	InstallationID    string         `json:"installation_id"`
	GenerationID      string         `json:"generation_id"`
	ControlCredential string         `json:"control_credential"`
	GatewayUsername   string         `json:"gateway_username"`
	GatewayCredential string         `json:"gateway_credential"`
	GatewayLocation   string         `json:"gateway_location"`
	PolicyRevisionID  string         `json:"policy_revision_id"`
	Policy            map[string]any `json:"policy"`
	SiteID            string         `json:"site_id"`
}

type HeartbeatResult struct {
	ServerTime              string         `json:"server_time"`
	PollSeconds             int            `json:"poll_seconds"`
	DesiredPolicyRevisionID string         `json:"desired_policy_revision_id"`
	Policy                  map[string]any `json:"policy"`
	PauseNewWANAdmissions   bool           `json:"pause_new_wan_admissions"`
	ProviderUnavailable     bool           `json:"provider_unavailable"`
	CompiledUploadKiBps     int            `json:"compiled_upload_kibps"`
	SiteID                  string         `json:"site_id"`
	AgentSHA256             string         `json:"agent_sha256"`
	AgentQualified          bool           `json:"agent_qualified"`
}

type AdmissionResult struct {
	SchemaVersion        int    `json:"schema_version"`
	Status               string `json:"status"`
	LeaseID              string `json:"lease_id"`
	Slot                 int    `json:"slot"`
	Class                string `json:"class"`
	BandwidthKiBps       int    `json:"bandwidth_kibps"`
	RestoreDownloadKiBps int    `json:"restore_download_kibps"`
	ExpiresAt            string `json:"expires_at"`
	QueuePosition        int    `json:"queue_position"`
	Message              string `json:"message"`
}

func (a *AdmissionResult) Granted() bool {
	return a != nil && a.Status == domain.AdmissionGranted && a.LeaseID != ""
}

// WANStartBlockReason is empty when a REST/Drive backup may request a lease.
// Non-empty values are admission statuses, not restic failures.
func WANStartBlockReason(e *Executor, pol domain.LocalPolicy, kind domain.BackendKind) string {
	if !domain.RequiresWANRateLimit(kind) {
		return ""
	}
	if e == nil || e.Client == nil || e.Client.BaseURL == "" {
		return domain.AdmissionControlUnavailable
	}
	if e.PauseWAN {
		return domain.AdmissionPaused
	}
	if e.ProviderDown {
		return domain.AdmissionProviderUnavailable
	}
	if e.PolicyInvalid {
		return domain.AdmissionDenied
	}
	if err := pol.ValidateForBackend(kind); err != nil {
		return domain.AdmissionSiteUnmeasured
	}
	return ""
}

type PolicyResult struct {
	RevisionID string         `json:"revision_id"`
	Config     map[string]any `json:"config"`
}

type CommandEnvelope struct {
	SchemaVersion int            `json:"schema_version"`
	CommandID     string         `json:"command_id"`
	JobID         string         `json:"job_id"`
	Kind          string         `json:"kind"`
	Payload       map[string]any `json:"payload"`
	PayloadHash   string         `json:"payload_hash"`
	ExpiresAt     string         `json:"expires_at"`
}
