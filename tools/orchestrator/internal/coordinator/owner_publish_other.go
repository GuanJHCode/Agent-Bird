//go:build !darwin && !linux

package coordinator

import "errors"

func ownerRenameNoReplace(parent int, old, new string) error {
	return errors.New("owner_key_publication_unsupported")
}
