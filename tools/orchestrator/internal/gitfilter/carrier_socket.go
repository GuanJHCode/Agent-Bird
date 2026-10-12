package gitfilter

import (
	"context"
	"errors"
	"strings"
)

var errCarrierSocket = errors.New("carrier_socket_unknown")

type carrierSocketBinding struct {
	Original                    StreamBinding
	ReservationID, Scope        string
	RuntimeNamespace            string
	DirectorySlots, SocketSlots int
}

// These records are scalar snapshots, never an authority or a cold-owner format.
type carrierSocketIdentity struct {
	Device, Inode, Nlink uint64
	UID, Mode            uint32
}

type carrierSocketRecord struct {
	Phase, Directory, Socket, Basename string
	Binding                            carrierSocketBinding
	Segment, Parent, Owned, SocketID   carrierSocketIdentity
	Known, Unknown                     bool
}

type carrierSocketJournal interface {
	RecordCarrierSocket(context.Context, carrierSocketRecord) error
}

// Both pointers are immutable. Copies reject before touching the original state.
type carrierSocket struct {
	self  *carrierSocket
	state *carrierSocketState
}

func carrierSocketContext(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, bounded := ctx.Deadline()
	return bounded
}

func (b carrierSocketBinding) valid() bool {
	for _, s := range []string{b.Original.ManifestSHA256, b.Original.ScopeSHA256, b.Original.ReservationID, b.Original.AttemptID, b.ReservationID, b.Scope} {
		if s == "" || len(s) > 256 || strings.IndexByte(s, 0) >= 0 {
			return false
		}
	}
	return b.Original.PlanRevision > 0 && b.ReservationID != b.Original.ReservationID && b.Scope != b.Original.ScopeSHA256 && b.RuntimeNamespace == "darwin-private-tmp-v1" && b.DirectorySlots == 1 && b.SocketSlots == 1
}

func (o *carrierSocket) original() bool { return o != nil && o.self == o && o.state != nil }
