//go:build !windows

package main

import "net/http"

// The agent runs on Windows; elsewhere nobody can be identified, so the
// local page serves no one's files.
func resolveLocalCaller(r *http.Request) (localCaller, error) { return localCaller{}, errCallerUnknown }

func grantReadTo(dir, sid string) error { return nil }
