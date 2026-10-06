//go:build !darwin && !linux

package install

import "errors"

func publicationRenameNoReplace(int, string, int, string) error {
	return errors.New("install_publication_unsupported")
}
