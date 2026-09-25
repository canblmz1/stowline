package service

import "context"

// QualPoller is an optional SCM runner hook for qualification-only local requests.
// It must not exist on the control plane.
type QualPoller interface {
	PollQualification(ctx context.Context) error
}
