package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// BackendKind identifies a storage profile. Capabilities are not assumed equal.
type BackendKind string

const (
	BackendPilotRcloneDrive BackendKind = "PILOT_RCLONE_DRIVE"
	BackendPilotRcloneLocal BackendKind = "PILOT_RCLONE_LOCAL"
	BackendRESTGateway      BackendKind = "REST_GATEWAY"
	BackendS3               BackendKind = "S3"
	BackendB2               BackendKind = "B2"
	BackendLocal            BackendKind = "LOCAL"
)

// Capabilities are explicit. Missing required capability fails provisioning.
type Capabilities struct {
	Read                     bool
	Write                    bool
	List                     bool
	AppendRestrictions       bool
	Deletion                 bool
	Overwrite                bool
	LockSupport              bool
	Maintenance              bool
	ImmutabilityQualified    bool
	QuotaScope               string
	QualificationOnly        bool
	AppendOnlyIsNotImmutable bool
}

// RepositoryDescriptor is the typed, validated binding. No provider branching in domain workflows.
type RepositoryDescriptor struct {
	BackendKind      BackendKind
	Location         string
	ExpectedRepoID   string
	GenerationID     string
	DeviceID         string
	TransportProfile string
	CredentialRefs   map[string]SecretRef
	TLS              TLSConfig
	Capabilities     Capabilities
}

// TLSConfig forbids insecure skip-verify.
type TLSConfig struct {
	MinVersion         string
	InsecureSkipVerify bool
	PinnedCAPath       string
}

func (d RepositoryDescriptor) Validate() error {
	if d.BackendKind == "" {
		return fmt.Errorf("%w: backend_kind required", ErrConfig)
	}
	switch d.BackendKind {
	case BackendPilotRcloneDrive, BackendPilotRcloneLocal, BackendRESTGateway, BackendS3, BackendB2, BackendLocal:
	default:
		return fmt.Errorf("%w: unknown backend_kind %q", ErrConfig, d.BackendKind)
	}
	if strings.TrimSpace(d.Location) == "" {
		return fmt.Errorf("%w: location required", ErrConfig)
	}
	if d.TLS.InsecureSkipVerify {
		return ErrTLSVerification
	}
	if looksLikeCredentialURL(d.Location) {
		return fmt.Errorf("%w: credentials must not appear in repository URLs", ErrConfig)
	}
	if !d.Capabilities.Read || !d.Capabilities.Write {
		return fmt.Errorf("%w: backup repository requires read and write", ErrCapabilityUnsupported)
	}
	if d.BackendKind == BackendRESTGateway && !d.Capabilities.LockSupport {
		return fmt.Errorf("%w: REST gateway requires lock support", ErrCapabilityUnsupported)
	}
	return nil
}

func looksLikeCredentialURL(location string) bool {
	if strings.Contains(location, "@") && strings.Contains(location, "://") {
		return true
	}
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	return u.User != nil
}

func DefaultCapabilities(kind BackendKind) Capabilities {
	switch kind {
	case BackendLocal, BackendPilotRcloneLocal:
		return Capabilities{
			Read: true, Write: true, List: true, LockSupport: true,
			Deletion: true, Overwrite: true, Maintenance: true,
			QualificationOnly:        true,
			AppendOnlyIsNotImmutable: true,
			QuotaScope:               "local-disk",
		}
	case BackendPilotRcloneDrive:
		return Capabilities{
			Read: true, Write: true, List: true, LockSupport: true,
			Deletion: true, Overwrite: true, Maintenance: false,
			QualificationOnly:        true,
			AppendOnlyIsNotImmutable: true,
			QuotaScope:               "workspace-shared-drive",
		}
	case BackendRESTGateway:
		return Capabilities{
			Read: true, Write: true, List: false, LockSupport: true,
			AppendRestrictions: true, Deletion: false, Overwrite: false,
			Maintenance: false, ImmutabilityQualified: false,
			AppendOnlyIsNotImmutable: true,
			QuotaScope:               "gateway-route",
		}
	case BackendS3, BackendB2:
		return Capabilities{
			Read: true, Write: true, List: true, LockSupport: true,
			Deletion: true, Overwrite: true, Maintenance: false,
			ImmutabilityQualified:    false,
			AppendOnlyIsNotImmutable: true,
			QuotaScope:               "bucket",
		}
	default:
		return Capabilities{}
	}
}
