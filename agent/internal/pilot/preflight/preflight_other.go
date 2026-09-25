//go:build !windows

package preflight

func vssCheck() Check {
	return Check{"vss_service", BLOCKED, "Windows only"}
}

func volumeFree(string) (uint64, error) {
	return 0, errNotWindows
}

var errNotWindows = errStr("windows only")

type errStr string

func (e errStr) Error() string { return string(e) }
