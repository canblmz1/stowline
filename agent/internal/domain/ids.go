package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

type DeviceID string
type InstallationID string
type JobID string
type AttemptID string
type SnapshotID string
type GenerationID string
type CorrelationID string

func NewID(prefix string) string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s%s-%s-%s-%s-%s", prefix,
		hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32])
}

func NewUUID() string { return NewID("") }

func NewJobID() JobID         { return JobID(NewUUID()) }
func NewAttemptID() AttemptID { return AttemptID(NewUUID()) }
func NewCorrelationID() CorrelationID {
	return CorrelationID(NewUUID())
}

// SlotKey is the logical backup identity: retries keep this key.
func SlotKey(deviceID, installationID, scheduleEpoch string, due time.Time) string {
	return fmt.Sprintf("%s|%s|%s|%s", deviceID, installationID, scheduleEpoch, due.UTC().Format(time.RFC3339))
}
