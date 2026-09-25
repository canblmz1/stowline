package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const (
	evStart     uint32 = 1
	evStop      uint32 = 2
	evTickError uint32 = 10
	evTickWarn  uint32 = 11
	evTickOK    uint32 = 12
	evCommand   uint32 = 13
)

// elog is the minimal event sink the service uses. eventlog.Log satisfies it;
// tests can pass a recorder.
type elog interface {
	Info(eid uint32, msg string) error
	Warning(eid uint32, msg string) error
	Error(eid uint32, msg string) error
}

type nopElog struct{}

func (nopElog) Info(uint32, string) error    { return nil }
func (nopElog) Warning(uint32, string) error { return nil }
func (nopElog) Error(uint32, string) error   { return nil }

// TickError is an operator-visible terminal backup result. It never includes secrets.
type TickError struct {
	Outcome      domain.AttemptPhase
	Class        domain.ErrorClass
	AttemptID    string
	JobID        string
	AttemptClass domain.AttemptClass
}

func (e *TickError) kindLabel() string {
	if e != nil && e.AttemptClass == domain.AttemptQualification {
		return "Qualification backup"
	}
	return "Scheduled backup"
}

func (e *TickError) Error() string {
	if e == nil {
		return "scheduled backup tick failed"
	}
	id := e.AttemptID
	if id == "" {
		id = "unknown"
	}
	class := e.Class
	if class == "" {
		class = domain.ErrorInternal
	}
	label := e.kindLabel()
	switch e.Outcome {
	case domain.PhaseSucceeded:
		return fmt.Sprintf("%s SUCCEEDED attempt=%s", label, id)
	case domain.PhaseFailed:
		return fmt.Sprintf("%s FAILED: class=%s attempt=%s", label, class, id)
	case domain.PhasePartial:
		return fmt.Sprintf("%s PARTIAL: class=%s attempt=%s", label, class, id)
	case domain.PhaseCancelled:
		return fmt.Sprintf("%s CANCELLED: class=%s attempt=%s", label, class, id)
	default:
		return fmt.Sprintf("%s %s: class=%s attempt=%s", label, e.Outcome, class, id)
	}
}

// WrapBackup turns a journaled backup result into a TickError even when the Go
// error is nil. AUTH FAILED must be visible to the Windows Service Event Log.
func WrapBackup(res domain.BackupResult, runErr error) error {
	if res.Outcome == "" {
		return runErr
	}
	te := &TickError{
		Outcome:      res.Outcome,
		Class:        res.ErrorClass,
		AttemptID:    string(res.AttemptID),
		JobID:        string(res.JobID),
		AttemptClass: res.AttemptClass.Effective(),
	}
	if te.Class == "" && res.Outcome != domain.PhaseSucceeded {
		te.Class = domain.ErrorInternal
	}
	return te
}

// WrapOperatorBackup is WrapBackup's counterpart for an operator-triggered
// "Backup Now" command. WrapBackup deliberately returns a non-nil *TickError
// even on success, so ReportTick's Outcome switch can still log a
// PhaseSucceeded tick as an Info-level Event Log line through the same
// "error" return value Tick() already threads through. A command
// dispatcher has no such switch: it acks the command FAILED for any non-nil
// error, so it must see plain nil-means-success semantics instead. Confirmed
// live: a real, durable, CONSISTENCY_VERIFIED backup was acked to the
// operator as error_class=INTERNAL on every attempt because WrapBackup's
// success value is still a non-nil error to that caller.
func WrapOperatorBackup(res domain.BackupResult, runErr error) error {
	if res.Outcome == domain.PhaseSucceeded {
		return nil
	}
	return WrapBackup(res, runErr)
}

// ReportTick writes an operator Event Log entry for a scheduler tick.
// Identical consecutive failure messages are suppressed. SCM cancellation is
// not logged as Error. Secrets must never appear in the message.
func ReportTick(log elog, ctx context.Context, err error, last *atomic.Pointer[string]) {
	if log == nil {
		log = nopElog{}
	}
	// scheduleAdapter can report both a control-command Poll error and a
	// scheduler/backup error from the same tick. Process every joined cause
	// so neither failure becomes invisible merely because the other happened.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			ReportTick(log, ctx, cause, last)
		}
		return
	}
	var te *TickError
	if errors.As(err, &te) {
		msg := domain.Redact(te.Error())
		switch te.Outcome {
		case domain.PhaseSucceeded:
			_ = log.Info(evTickOK, msg)
			if last != nil {
				last.Store(nil)
			}
			return
		case domain.PhasePartial:
			emitDeduped(log, last, evTickWarn, false, msg)
			return
		case domain.PhaseCancelled:
			if ctx != nil && ctx.Err() != nil {
				_ = log.Info(evStop, domain.Redact(te.kindLabel()+" CANCELLED during service stop attempt="+safeAttempt(te.AttemptID)))
				return
			}
			emitDeduped(log, last, evTickWarn, false, msg)
			return
		case domain.PhaseFailed:
			emitDeduped(log, last, evTickError, true, msg)
			return
		default:
			emitDeduped(log, last, evTickError, true, msg)
			return
		}
	}
	if err == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	msg := domain.Redact(err.Error())
	emitDeduped(log, last, evTickError, true, "backup tick failed: "+msg)
}

func safeAttempt(id string) string {
	if id == "" {
		return "unknown"
	}
	return id
}

func emitDeduped(log elog, last *atomic.Pointer[string], eid uint32, isError bool, msg string) {
	if last != nil {
		if prev := last.Load(); prev != nil && *prev == msg {
			return
		}
		last.Store(&msg)
	}
	if isError {
		_ = log.Error(eid, msg)
		return
	}
	_ = log.Warning(eid, msg)
}
