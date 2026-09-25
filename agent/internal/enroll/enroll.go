// Package enroll applies control-plane enrollment to the local agent config
// and secret envelopes. It does not print credentials.
package enroll

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/config"
	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
)

const (
	ModePilot   = "pilot"
	ModeService = "service"

	controlUserName    = "control-credential.user.dpapi"
	controlMachineName = "control-credential.machine.dpapi"
	gatewayUserName    = "gateway-credential.user.dpapi"
	gatewayMachineName = "gateway-credential.machine.dpapi"
	gatewayUserFile    = "gateway-username.txt"
	probeUserName      = "enroll-preflight.user.dpapi.tmp"
	probeMachineName   = "enroll-preflight.machine.dpapi.tmp"
)

// Client is the control-plane enrollment surface. Implementations must not log secrets.
type Client interface {
	Enroll(ctx context.Context, token, hostname, version, installationID string) (*ctrlclient.EnrollResult, error)
	AbortEnrollment(ctx context.Context, controlCredential string) error
}

// Request is a local enrollment attempt. Token is consumed only after Preflight succeeds.
type Request struct {
	Mode           string
	URL            string
	Token          string
	Hostname       string
	Version        string
	InstallationID string
	Cfg            *config.File
	Client         Client
	PersistConfig  func(config.File) error
	Preflight      func(mode, secretsDir string) error
	ApplySecretACL func(dir, mode string) error
}

// Result is the non-secret enrollment summary. Credentials are never included.
type Result struct {
	DeviceID        string
	InstallationID  string
	GenerationID    string
	SiteID          string
	ControlPlaneURL string
	Mode            string
}

// ParseMode accepts pilot (default) or service. Service mode is never implied.
func ParseMode(raw string) (string, error) {
	m := strings.TrimSpace(strings.ToLower(raw))
	if m == "" || m == ModePilot {
		return ModePilot, nil
	}
	if m == ModeService {
		return ModeService, nil
	}
	return "", fmt.Errorf("%w: enroll --mode must be pilot or service", domain.ErrConfig)
}

// Preflight verifies local secret storage before the one-time token is consumed.
func Preflight(mode, secretsDir string) error {
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		return fmt.Errorf("%w: cannot create service secrets directory: %v", domain.ErrConfig, err)
	}
	probe := probeUserName
	var provider secrets.DPAPIProvider
	if mode == ModeService {
		probe = probeMachineName
		provider = secrets.NewService(secretsDir)
	} else {
		provider = secrets.NewPilot(secretsDir)
	}
	path := filepath.Join(secretsDir, probe)
	_ = os.Remove(path)
	plain := []byte("stowline-enroll-preflight")
	if err := provider.Protect(probe, plain); err != nil {
		zero(plain)
		kind := "enrollment"
		if mode == ModeService {
			kind = "service enrollment"
		}
		return fmt.Errorf("%w: %s cannot write secret envelopes (%v)", domain.ErrConfig, kind, err)
	}
	h, err := provider.Open(context.Background(), domain.SecretRef{
		Locator:  path,
		Provider: provider.Kind(),
	})
	if err != nil {
		_ = os.Remove(path)
		zero(plain)
		return fmt.Errorf("%w: enrollment probe decrypt failed: %v", domain.ErrSecretScopeMismatch, err)
	}
	ok := string(h.Bytes()) == string(plain)
	_ = h.Close()
	zero(plain)
	_ = os.Remove(path)
	if !ok {
		return fmt.Errorf("%w: enrollment probe round-trip mismatch", domain.ErrSecretScopeMismatch)
	}
	return nil
}

// Run performs local preflight, remote enroll, local persist, and best-effort
// abort if persistence fails after the control plane has created a device.
func Run(ctx context.Context, req Request) (*Result, error) {
	mode, err := ParseMode(req.Mode)
	if err != nil {
		return nil, err
	}
	if req.Cfg == nil {
		return nil, fmt.Errorf("%w: config required", domain.ErrConfig)
	}
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.Token) == "" {
		return nil, fmt.Errorf("%w: enroll --url and --token required", domain.ErrConfig)
	}
	if req.Client == nil {
		return nil, fmt.Errorf("%w: control client required", domain.ErrConfig)
	}
	dir := filepath.Join(req.Cfg.PilotRoot, "secrets")
	pre := req.Preflight
	if pre == nil {
		pre = Preflight
	}
	if err := pre(mode, dir); err != nil {
		return nil, err
	}

	out, err := req.Client.Enroll(ctx, req.Token, req.Hostname, req.Version, req.InstallationID)
	if err != nil {
		return nil, err
	}
	cred := out.ControlCredential
	defer func() {
		out.ControlCredential = ""
		out.GatewayCredential = ""
		zero([]byte(cred))
	}()

	if err := persistLocal(req, mode, dir, out); err != nil {
		if abortErr := req.Client.AbortEnrollment(ctx, cred); abortErr != nil {
			return nil, fmt.Errorf("local enrollment persistence failed (%w); remote abort also failed: %v", err, abortErr)
		}
		return nil, fmt.Errorf("local enrollment persistence failed; remote device aborted: %w", err)
	}
	return &Result{
		DeviceID:        out.DeviceID,
		InstallationID:  out.InstallationID,
		GenerationID:    out.GenerationID,
		SiteID:          out.SiteID,
		ControlPlaneURL: req.URL,
		Mode:            mode,
	}, nil
}

func persistLocal(req Request, mode, dir string, out *ctrlclient.EnrollResult) error {
	orig := *req.Cfg
	roots := append([]string(nil), orig.SourceRoots...)
	if err := Bind(req.Cfg, out, req.URL, mode); err != nil {
		*req.Cfg = orig
		return err
	}
	if err := writeEnvelopes(dir, mode, out, req.Cfg); err != nil {
		*req.Cfg = orig
		scrubEnrollmentFiles(dir, mode)
		return err
	}
	apply := req.ApplySecretACL
	if apply == nil {
		apply = defaultApplyACL
	}
	if err := apply(dir, mode); err != nil {
		*req.Cfg = orig
		scrubEnrollmentFiles(dir, mode)
		return err
	}
	req.Cfg.SourceRoots = roots
	persist := req.PersistConfig
	if persist == nil {
		*req.Cfg = orig
		scrubEnrollmentFiles(dir, mode)
		return fmt.Errorf("%w: persist config required", domain.ErrConfig)
	}
	if err := persist(*req.Cfg); err != nil {
		_ = persist(orig)
		*req.Cfg = orig
		scrubEnrollmentFiles(dir, mode)
		return err
	}
	return nil
}

// Bind copies server-issued identity onto cfg. It does not write secrets or consume tokens.
func Bind(cfg *config.File, out *ctrlclient.EnrollResult, controlURL, mode string) error {
	if cfg == nil || out == nil {
		return fmt.Errorf("%w: bind requires config and enroll result", domain.ErrConfig)
	}
	if out.DeviceID == "" {
		return fmt.Errorf("%w: incomplete enrollment", domain.ErrConfig)
	}
	mode, err := ParseMode(mode)
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	cfg.DeviceID = out.DeviceID
	cfg.InstallationID = out.InstallationID
	cfg.ControlPlaneURL = controlURL
	cfg.SiteID = out.SiteID
	ctrlName := controlUserName
	ctrlProv := domain.SecretProviderDPAPIUser
	if mode == ModeService {
		ctrlName = controlMachineName
		ctrlProv = domain.SecretProviderDPAPIMachine
	}
	cfg.ControlSecretRef = domain.SecretRef{
		Purpose:  "control-plane",
		Provider: ctrlProv,
		Locator:  filepath.Join(dir, ctrlName),
	}
	if out.GatewayLocation == "" {
		return nil
	}
	gwName := gatewayUserName
	gwProv := domain.SecretProviderDPAPIUser
	if mode == ModeService {
		gwName = gatewayMachineName
		gwProv = domain.SecretProviderDPAPIMachine
	}
	cfg.Repository.BackendKind = domain.BackendRESTGateway
	cfg.Repository.Location = out.GatewayLocation
	cfg.Repository.GenerationID = out.GenerationID
	cfg.Repository.DeviceID = out.DeviceID
	cfg.Repository.TransportProfile = "lab-insecure-http"
	cfg.Repository.Capabilities = domain.DefaultCapabilities(domain.BackendRESTGateway)
	cfg.Repository.Capabilities.QualificationOnly = true
	// A new enrollment always means a new device_id/generation_id/location --
	// a logically different repository whose real restic repository-id is
	// not known yet. Leaving a previous enrollment's ExpectedRepoID in place
	// here binds the brand-new (still uninitialized) repository to a stale
	// expectation from an unrelated earlier one on the same machine, and its
	// first backup fails closed with ErrRepositoryBinding before ever
	// running restic. Confirmed live: re-enrolling this physical endpoint
	// carried an old repository's ExpectedRepoID into the new one's config.
	// ops.go's own comment already documents the intended lifecycle: this
	// field is meant to auto-populate from the first real, successful read
	// of the (now genuinely new) repository, not be inherited.
	cfg.Repository.ExpectedRepoID = ""
	if cfg.Repository.CredentialRefs == nil {
		cfg.Repository.CredentialRefs = map[string]domain.SecretRef{}
	}
	cfg.Repository.CredentialRefs[domain.SecretRESTUsername] = domain.SecretRef{
		Purpose:  domain.SecretRESTUsername,
		Provider: domain.SecretProviderFile,
		Locator:  filepath.Join(dir, gatewayUserFile),
	}
	cfg.Repository.CredentialRefs[domain.SecretRESTPassword] = domain.SecretRef{
		Purpose:  domain.SecretRESTPassword,
		Provider: gwProv,
		Locator:  filepath.Join(dir, gwName),
	}
	return nil
}

func writeEnvelopes(dir, mode string, out *ctrlclient.EnrollResult, cfg *config.File) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var provider secrets.DPAPIProvider
	ctrlName, gwName := controlUserName, gatewayUserName
	if mode == ModeService {
		provider = secrets.NewService(dir)
		ctrlName, gwName = controlMachineName, gatewayMachineName
	} else {
		provider = secrets.NewPilot(dir)
	}
	if err := provider.Protect(ctrlName, []byte(out.ControlCredential)); err != nil {
		return fmt.Errorf("protect control credential: %w", err)
	}
	if out.GatewayCredential != "" {
		if err := provider.Protect(gwName, []byte(out.GatewayCredential)); err != nil {
			return fmt.Errorf("protect gateway credential: %w", err)
		}
	}
	if out.GatewayUsername != "" && cfg != nil && cfg.Repository.CredentialRefs != nil {
		if ref, ok := cfg.Repository.CredentialRefs[domain.SecretRESTUsername]; ok && ref.Locator != "" {
			if err := os.WriteFile(ref.Locator, []byte(out.GatewayUsername), 0600); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultApplyACL(dir, mode string) error {
	profile := acl.PilotWorking
	if mode == ModeService {
		profile = acl.ServiceSecret
	}
	if err := acl.Apply(dir, profile); err != nil {
		return fmt.Errorf("enrollment secret ACL: %w", err)
	}
	return nil
}

func scrubEnrollmentFiles(dir, mode string) {
	names := []string{controlUserName, gatewayUserName, gatewayUserFile, probeUserName, probeMachineName}
	if mode == ModeService {
		names = []string{controlMachineName, gatewayMachineName, gatewayUserFile, probeUserName, probeMachineName}
	}
	for _, n := range names {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// DiagnosticString is a redacted one-line summary. It must never include credentials.
func DiagnosticString(r *Result) string {
	if r == nil {
		return "enrollment incomplete"
	}
	return fmt.Sprintf("enrolled device_id=%s installation_id=%s generation_id=%s site_id=%s mode=%s (credentials stored, not printed)",
		r.DeviceID, r.InstallationID, r.GenerationID, r.SiteID, r.Mode)
}
