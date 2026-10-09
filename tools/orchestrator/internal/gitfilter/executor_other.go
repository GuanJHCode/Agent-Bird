//go:build !darwin

package gitfilter

import (
	"context"
	"errors"
)

func Execute(ctx context.Context, r Request) (Result, error) {
	return Result{ScopeSHA256: r.ScopeSHA256, ExitCode: -1}, errors.New("filter_platform_unsupported")
}
