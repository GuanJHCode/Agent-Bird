//go:build !darwin

package process

import "context"

func ObserveArguments(ctx context.Context, pid int) (ArgumentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ArgumentSnapshot{}, err
	}
	return ArgumentSnapshot{}, CodeError("process_arguments_unsupported")
}
