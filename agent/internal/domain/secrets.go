package domain

import "strings"

// SecretRef is an opaque handle. The value is never a log field.
type SecretRef struct {
	Purpose  string
	Provider string
	Locator  string
	Version  string
}

const (
	SecretResticPassword   = "restic-repository-password"
	SecretRcloneConfigPass = "rclone-config-password"
	SecretRESTUsername     = "restic-rest-username"
	SecretRESTPassword     = "restic-rest-password"

	SecretProviderFile         = "file"
	SecretProviderDPAPIUser    = "dpapi-user"
	SecretProviderDPAPIMachine = "dpapi-machine"

	SecretScopeUser    = "user"
	SecretScopeMachine = "machine"
)

// CanarySecret is used only in tests to detect leakage. It is not a real credential.
const CanarySecret = "STOWLINE_CANARY_SECRET_DO_NOT_LEAK_9f3a2c1b"

var secretEnvNames = []string{
	"RESTIC_PASSWORD",
	"RESTIC_PASSWORD_FILE",
	"RESTIC_PASSWORD_COMMAND",
	"RESTIC_FROM_PASSWORD",
	"RESTIC_FROM_PASSWORD_FILE",
	"RESTIC_FROM_PASSWORD_COMMAND",
	"RESTIC_REST_PASSWORD",
	"RESTIC_REST_USERNAME",
	"RCLONE_CONFIG_PASS",
	"RCLONE_PASSWORD",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"B2_ACCOUNT_KEY",
	"OS_PASSWORD",
	"AZURE_ACCOUNT_KEY",
}

func IsSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, n := range secretEnvNames {
		if upper == n {
			return true
		}
	}
	if strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") {
		if strings.Contains(upper, "RESTIC") || strings.Contains(upper, "RCLONE") || strings.Contains(upper, "OAUTH") || strings.Contains(upper, "AUTHORIZATION") {
			return true
		}
	}
	return false
}
