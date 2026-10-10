package gitfilter

import (
	"context"
	"errors"
)

var errCarrierSocket = errors.New("carrier_socket_unknown")

type carrierSocketBinding struct {
	Original StreamBinding
	ReservationID, Scope string
	RuntimeNamespace     string
	DirectorySlots, SocketSlots int
}

type carrierSocketRecord struct { Phase, Directory, Socket string; Binding carrierSocketBinding }
type carrierSocketJournal interface { RecordCarrierSocket(context.Context, carrierSocketRecord) error }
type carrierSocket struct{}

func newCarrierSocket(context.Context, string, carrierSocketBinding, carrierSocketJournal) (*carrierSocket, error) { return nil, errCarrierSocket }
func (*carrierSocket) close(context.Context) error { return errCarrierSocket }
func (*carrierSocket) remove(context.Context) error { return errCarrierSocket }
