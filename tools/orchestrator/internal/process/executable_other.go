//go:build !darwin

package process

// ExecutablePath fails closed until the platform has verified peer identity support.
func ExecutablePath(pid int) (string, error) {
	return "", CodeError("process_executable_platform_unsupported")
}
