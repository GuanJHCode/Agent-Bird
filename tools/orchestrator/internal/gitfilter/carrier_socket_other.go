//go:build !darwin

package gitfilter

import "context"

type carrierSocketState struct{}

func newCarrierSocket(context.Context, string, carrierSocketBinding, carrierSocketJournal) (*carrierSocket, error) {
	return nil, errCarrierSocket
}
func (*carrierSocket) verify(context.Context) error { return errCarrierSocket }
func (*carrierSocket) close(context.Context) error  { return errCarrierSocket }
func (*carrierSocket) remove(context.Context) error { return errCarrierSocket }
