//go:build !darwin

package process

func IsCodexAppServerDaemon(pid int, expectedExecutable string) (bool, error) {
	return false, CodeError("process_role_platform_unsupported")
}
