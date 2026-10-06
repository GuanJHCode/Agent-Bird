package host

import "errors"

// Issued only by the dedicated launcher when Start did not succeed. It is not
// serializable proof and cannot be reconstructed from a persisted PID/tuple.
type ownerTransportNoStartError struct {
	scope       string
	reservation ownerRecoveryPending
	cause       error
}

func (e *ownerTransportNoStartError) Error() string { return e.cause.Error() }
func (e *ownerTransportNoStartError) Unwrap() error { return e.cause }

// The owner control loop serializes these transitions with all reservations.
// The child Wait goroutine only closes done; it never mutates this ledger.
func (s *ownerRecoveryState) retireTransportNoStart(err error) error {
	var proof *ownerTransportNoStartError
	p := s.record.OwnerPending
	if !errors.As(err, &proof) || proof == nil || proof.scope != s.record.ScopeSHA256 || p == nil ||
		(p.Phase != "reserved" && p.Phase != "starting") || p.NewHost != nil ||
		proof.reservation != *p {
		return ownerRecoveryRejected()
	}
	return s.retirePendingTransport()
}

func (s *ownerRecoveryState) retireTransportAfterWait(child *ownerTransportChild) error {
	p := s.record.OwnerPending
	if !child.waited() || p == nil || child.recoveryScope != s.record.ScopeSHA256 ||
		child.reservation.Phase != "starting" || child.reservation.NewHost != nil ||
		child.reservation.StartSlot != p.StartSlot || child.reservation.RequestID != p.RequestID ||
		child.reservation.ExpectedControlRevision != p.ExpectedControlRevision || child.reservation.OldHost != p.OldHost {
		return ownerRecoveryRejected()
	}
	switch p.Phase {
	case "starting", "unknown":
		if p.NewHost != nil {
			return ownerRecoveryRejected()
		}
	case "started", "attached", "applied", "activated":
		if p.NewHost == nil || *p.NewHost != child.identity {
			return ownerRecoveryRejected()
		}
	default:
		return ownerRecoveryRejected()
	}
	return s.retirePendingTransport()
}

func (s *ownerRecoveryState) retirePendingTransport() error {
	next := s.copyRecord()
	if r := next.Reservation; r != nil && r.Kind == "transport_start" {
		if r.Slot != next.OwnerPending.StartSlot {
			return ownerRecoveryRejected()
		}
		next.Reservation = nil
	}
	next.OwnerPending = nil
	// Attempts/backoff, an independent Coordinator reservation, and Unavailable
	// remain untouched. commit also keeps failed persistence permanently closed.
	return s.commit(next)
}
