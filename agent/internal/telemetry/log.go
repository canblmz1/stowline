package telemetry

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

type Logger struct {
	w io.Writer
}

func New(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{w: w}
}

type Record struct {
	Timestamp      string `json:"timestamp"`
	Severity       string `json:"severity"`
	Component      string `json:"component"`
	DeviceID       string `json:"device_id,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	AttemptID      string `json:"attempt_id,omitempty"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	EventType      string `json:"event_type"`
	ErrorClass     string `json:"error_class,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	Message        string `json:"message,omitempty"`
}

func (l *Logger) Emit(r Record) {
	r.Message = domain.Redact(r.Message)
	if r.Timestamp == "" {
		r.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = l.w.Write(append(b, '\n'))
}

func (l *Logger) Info(component, event, msg string) {
	l.Emit(Record{Severity: "INFO", Component: component, EventType: event, Message: msg})
}

func (l *Logger) Error(component, event string, class domain.ErrorClass, msg string) {
	l.Emit(Record{Severity: "ERROR", Component: component, EventType: event, ErrorClass: string(class), Message: msg})
}
