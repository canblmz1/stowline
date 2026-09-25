//go:build !windows

package acl

type Profile string

const (
	PilotWorking   Profile = "pilot-working"
	ServiceSecret  Profile = "service-secret"
	ServiceState   Profile = "service-state"
	RestoreStaging Profile = "restore-staging"
)

func RestrictToAdminSystemAndOwner(path string) error { return Apply(path, PilotWorking) }

func Apply(string, Profile) error { return nil }

func Inspect(string) (Inspection, error) { return Inspection{}, nil }
