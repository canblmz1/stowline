//go:build !windows

package main

func grantUsersRead(dir string) error { return nil }
