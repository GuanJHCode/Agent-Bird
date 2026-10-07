package host

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

var errDiagnosticsUnavailable = errors.New("diagnostics_unavailable")

// Execution-goroutine state only. The driver callback merely cancels the
// original execution; it never mutates this state or takes process ownership.
type executionDiagnostics struct {
	driver                  *diagnosticDriver
	summaryErr, detailedErr error
	cleanupDeadline         time.Time
	proved                  atomic.Bool
}

func (d *executionDiagnostics) wakeExecution(cancel context.CancelCauseFunc) func(error) {
	return func(error) {
		if !d.proved.Load() {
			cancel(errDiagnosticsUnavailable)
		}
	}
}

func (d *executionDiagnostics) end(proved bool, cause error) error {
	if proved {
		d.proved.Store(true)
	}
	if d.driver != nil {
		if d.cleanupDeadline.IsZero() {
			d.cleanupDeadline = time.Now().Add(diagnosticOperationTimeout)
		}
		ctx, cancel := context.WithDeadline(context.Background(), d.cleanupDeadline)
		var err error
		if proved && d.summaryErr == nil {
			err = d.driver.Finish(ctx)
		} else {
			err = d.driver.Abort(ctx, errors.Join(cause, d.summaryErr))
		}
		cancel()
		if err != nil && d.detailedErr == nil {
			d.detailedErr = err
		}
	}
	if d.summaryErr == nil && d.detailedErr == nil {
		return nil
	}
	return errors.Join(errDiagnosticsUnavailable, d.summaryErr, d.detailedErr)
}

func (d *executionDiagnostics) failure() *contract.DiagnosticsFailure {
	if d.summaryErr == nil && d.detailedErr == nil {
		return nil
	}
	return &contract.DiagnosticsFailure{Version: 1, Summary: d.summaryErr != nil, Detailed: d.detailedErr != nil}
}
