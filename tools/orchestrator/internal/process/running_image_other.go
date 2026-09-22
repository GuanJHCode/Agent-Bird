//go:build !darwin

package process

import "context"

// VerifyRunningExecutable is unavailable until this platform has an equivalent
// kernel-backed executable vnode query.
func VerifyRunningExecutable(context.Context, Identity, string, string) error {
	return CodeError("running_executable_platform_unsupported")
}
