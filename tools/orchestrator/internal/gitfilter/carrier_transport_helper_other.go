//go:build !darwin

package gitfilter

import (
	"context"
	"os"
)

func runCarrierHelper(context.Context, CarrierHelperRoute, *os.File, *os.File) error {
	return ErrCarrierHelper
}
