package service

import (
	"context"
	"errors"
)

type Runner interface {
	RunOnce(context.Context) error
}

// Startup runs inside the registered SCM handler, before any application work.
// ErrRestartRequired means the guard restored the previous executable; no
// runner may be prepared or started in that process.
type Startup func() (Runner, error)

var ErrRestartRequired = errors.New("unconfirmed upgrade rolled back; SCM restart required")
