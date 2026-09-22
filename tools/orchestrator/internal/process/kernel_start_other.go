//go:build !darwin

package process

func KernelStartID(int) (string, error) {
	return "", CodeError("kernel_start_platform_unsupported")
}
