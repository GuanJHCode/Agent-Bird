//go:build !darwin && !linux

package host

func initialOwnerRenameNoReplace(fd int, old, next string) error {
	return ownerRecoveryRejected()
}
