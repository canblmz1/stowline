//go:build embedpayload

package main

import _ "embed"

// payload.zip is the whole installer package (deploy.json, payload\...),
// written here by scripts/package-installer.py right before it
// builds the single-file Stowline Setup.exe. Never committed.
//
//go:embed payload.zip
var embeddedPayload []byte
