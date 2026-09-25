//go:build !windows

package identity

func Elevated() bool { return false }
