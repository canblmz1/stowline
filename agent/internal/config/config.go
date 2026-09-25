package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

type File struct {
	DeviceID         string                      `json:"device_id"`
	InstallationID   string                      `json:"installation_id"`
	PilotRoot        string                      `json:"pilot_root"`
	Binaries         Binaries                    `json:"binaries"`
	Repository       domain.RepositoryDescriptor `json:"repository"`
	PasswordRef      domain.SecretRef            `json:"password_ref"`
	SourceRoots      []string                    `json:"source_roots"`
	ExcludePaths     []string                    `json:"exclude_paths,omitempty"`
	StagingRoot      string                      `json:"staging_root"`
	JournalPath      string                      `json:"journal_path"`
	CacheDir         string                      `json:"cache_dir"`
	TempDir          string                      `json:"temp_dir"`
	VSSMode          domain.VSSMode              `json:"vss_mode"`
	RcloneConfigPath string                      `json:"rclone_config_path"`
	ControlPlaneURL  string                      `json:"control_plane_url,omitempty"`
	ControlSecretRef domain.SecretRef            `json:"control_secret_ref,omitempty"`
	SiteID           string                      `json:"site_id,omitempty"`
}

type Binaries struct {
	Restic domain.BinaryPin `json:"restic"`
	Rclone domain.BinaryPin `json:"rclone"`
}

// StripBOM drops a leading UTF-8 byte order mark. Windows PowerShell 5.1 and
// older Notepad write one when saving UTF-8, and encoding/json rejects it.
func StripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = StripBOM(b)
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) Validate() error {
	if f.Binaries.Restic.Path == "" || f.Binaries.Restic.SHA256 == "" {
		return fmt.Errorf("%w: restic pin required", domain.ErrConfig)
	}
	if f.Repository.BackendKind == "" {
		return fmt.Errorf("%w: repository required", domain.ErrConfig)
	}
	if err := f.Repository.Validate(); err != nil {
		return err
	}
	if f.VSSMode == "" {
		f.VSSMode = domain.VSSRequired
	}
	if f.VSSMode != domain.VSSRequired && f.VSSMode != domain.VSSDisabled {
		return fmt.Errorf("%w: invalid vss_mode", domain.ErrConfig)
	}
	if f.ControlPlaneURL != "" {
		if strings.Contains(f.ControlPlaneURL, "@") {
			return fmt.Errorf("%w: credentials must not appear in control-plane URLs", domain.ErrConfig)
		}
	}
	return nil
}

func DefaultPilot() File {
	root := `C:\Stowline`
	return File{
		PilotRoot:      root,
		DeviceID:       "pilot-device",
		InstallationID: "pilot-install",
		Binaries: Binaries{
			Restic: domain.BinaryPin{
				Product: "restic",
				Version: "0.19.1",
				Path:    root + `\bin\restic.exe`,
				SHA256:  "b0dd1fd21eea5d8fe1325f55f7118213c21f36de8a261e04c0624a5ab9fd7830",
			},
			Rclone: domain.BinaryPin{
				Product: "rclone",
				Version: "1.75.0",
				Path:    root + `\bin\rclone.exe`,
				SHA256:  "8be30f02266a6eaad9d481941ef287b9744bb7140034b097ad862a2ccff3e24c",
			},
		},
		Repository: domain.RepositoryDescriptor{
			BackendKind:      domain.BackendLocal,
			Location:         root + `\Repos\local`,
			GenerationID:     "pilot-gen-1",
			DeviceID:         "pilot-device",
			Capabilities:     domain.DefaultCapabilities(domain.BackendLocal),
			TransportProfile: "local-qualification",
		},
		PasswordRef: domain.SecretRef{
			Purpose:  domain.SecretResticPassword,
			Provider: domain.SecretProviderDPAPIUser,
			Locator:  root + `\secrets\restic-password.user.dpapi`,
		},
		SourceRoots: []string{root + `\TestCorpus`},
		StagingRoot: root + `\Restore`,
		JournalPath: root + `\state\journal.sqlite`,
		CacheDir:    root + `\cache`,
		TempDir:     root + `\cache\tmp`,
		VSSMode:     domain.VSSRequired,
	}
}
