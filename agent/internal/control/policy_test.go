package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/journal"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

func samplePolicy(upload int) map[string]any {
	return map[string]any{
		"schema_version": 1.0,
		"source_roots":   []any{`C:\Stowline\TestCorpus`},
		"vss_mode":       "required",
		"schedule": map[string]any{
			"timezone":               "Europe/Istanbul",
			"eligibility_start":      "08:30",
			"window_start":           "12:00",
			"window_end":             "16:30",
			"hard_stop":              "17:45",
			"catch_up":               true,
			"max_attempts":           3.0,
			"retry_delay_1_minutes":  15.0,
			"retry_delay_2_minutes":  60.0,
			"boot_delay_min_minutes": 5.0,
			"boot_delay_max_minutes": 20.0,
		},
		"bandwidth": map[string]any{
			"upload_limit_kibps":           float64(upload),
			"restore_download_limit_kibps": 122.0,
			"lab_local_unlimited":          false,
		},
	}
}

func TestPolicyFromServerCompiledCeiling(t *testing.T) {
	p, err := PolicyFromServer("rev-1", samplePolicy(439))
	if err != nil {
		t.Fatal(err)
	}
	if p.BandwidthKiBps != 439 || p.RestoreDownloadKiBps != 122 {
		t.Fatalf("got upload=%d download=%d", p.BandwidthKiBps, p.RestoreDownloadKiBps)
	}
	if err := p.ValidateForBackend(domain.BackendRESTGateway); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyFromServerMalformedFailsClosed(t *testing.T) {
	cfg := samplePolicy(439)
	cfg["vss_mode"] = "maybe"
	if _, err := PolicyFromServer("rev-1", cfg); err == nil {
		t.Fatal("malformed vss_mode must fail closed")
	}
}

func TestWANStartBlockReasonControlPlaneDown(t *testing.T) {
	pol := domain.DefaultPilotPolicy([]string{`C:\x`})
	pol.LabLocalUnlimited = false
	pol.BandwidthKiBps = 439
	if got := WANStartBlockReason(nil, pol, domain.BackendRESTGateway); got != domain.AdmissionControlUnavailable {
		t.Fatalf("got %s", got)
	}
	e := &Executor{Client: &Client{}}
	if got := WANStartBlockReason(e, pol, domain.BackendRESTGateway); got != domain.AdmissionControlUnavailable {
		t.Fatalf("empty base URL got %s", got)
	}
	e.Client.BaseURL = "https://cp.stowline.local"
	e.PauseWAN = true
	if got := WANStartBlockReason(e, pol, domain.BackendRESTGateway); got != domain.AdmissionPaused {
		t.Fatalf("got %s", got)
	}
	e.PauseWAN = false
	e.ProviderDown = true
	if got := WANStartBlockReason(e, pol, domain.BackendRESTGateway); got != domain.AdmissionProviderUnavailable {
		t.Fatalf("got %s", got)
	}
	if WANStartBlockReason(e, pol, domain.BackendLocal) != "" {
		t.Fatal("LOCAL must not require WAN admission")
	}
}

func TestHeartbeatAppliesPolicy(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var applied atomic.Int32
	var last int
	body, _ := json.Marshal(map[string]any{
		"schema_version": 1, "poll_seconds": 20,
		"desired_policy_revision_id": "rev-hb",
		"policy":                     samplePolicy(439),
		"agent_sha256":               "deadbeef",
		"agent_qualified":            true,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/heartbeat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case r.URL.Path == "/api/v1/agent/work/claim":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"command":null}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	var gotSHA string
	var gotQualified bool
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		OnPolicy: func(p domain.LocalPolicy) {
			applied.Add(1)
			last = p.BandwidthKiBps
		},
		OnAgentPin: func(sha256 string, qualified bool) {
			gotSHA, gotQualified = sha256, qualified
		},
	}
	if err := e.Poll(context.Background(), "none"); err != nil {
		t.Fatal(err)
	}
	if applied.Load() != 1 || last != 439 {
		t.Fatalf("heartbeat must apply compiled policy, applied=%d kibps=%d", applied.Load(), last)
	}
	if gotSHA != "deadbeef" || !gotQualified {
		t.Fatalf("heartbeat must forward the agent pin, got sha=%s qualified=%v", gotSHA, gotQualified)
	}
}

func TestRefreshPolicyFetchesAndApplies(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var applied atomic.Int32
	polBody, _ := json.Marshal(map[string]any{"schema_version": 1, "revision_id": "rev-rf", "config": samplePolicy(439)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/heartbeat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"poll_seconds":20}`))
		case r.URL.Path == "/api/v1/agent/work/claim":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version": 1,
				"command":        map[string]any{"command_id": "c1", "job_id": "j1", "kind": "REFRESH_POLICY", "payload": map[string]any{}},
			})
		case r.URL.Path == "/api/v1/agent/policy":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(polBody)
		case strings.HasPrefix(r.URL.Path, "/api/v1/agent/commands/"):
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "SUCCEEDED") {
				t.Errorf("ack=%s", b)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	e := &Executor{
		Client:  &Client{BaseURL: srv.URL, Credential: "tok"},
		Journal: j,
		OnPolicy: func(p domain.LocalPolicy) {
			applied.Add(1)
		},
	}
	if err := e.Poll(context.Background(), "none"); err != nil {
		t.Fatal(err)
	}
	if applied.Load() != 1 {
		t.Fatalf("REFRESH_POLICY must apply policy, got %d", applied.Load())
	}
	if e.PolicyRev != "rev-rf" {
		t.Fatalf("revision=%s", e.PolicyRev)
	}
}
