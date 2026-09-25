//go:build !embedpayload

package main

// Built without a payload (build-release.ps1): the exe expects the package
// folder next to it, the way Setup.cmd does.
var embeddedPayload []byte
