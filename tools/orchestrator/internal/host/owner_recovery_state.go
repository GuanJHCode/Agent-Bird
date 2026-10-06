package host

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type ownerRecoveryReservation struct {
	Slot  int    `json:"slot"`
	Kind  string `json:"kind"`
	Phase string `json:"phase"`
}
type ownerRecoveryPending struct {
	StartSlot               int                              `json:"start_slot"`
	RequestID               string                           `json:"request_id"`
	ExpectedControlRevision uint64                           `json:"expected_control_revision"`
	OldHost                 contract.OriginalOwnerTransport  `json:"old_host"`
	NewHost                 *contract.OriginalOwnerTransport `json:"new_host,omitempty"`
	Phase                   string                           `json:"phase"`
}
type ownerRecoveryRecord struct {
	Version           int                       `json:"version"`
	AuthoritySHA256   string                    `json:"authority_sha256"`
	ScopeSHA256       string                    `json:"scope_sha256"`
	Attempts          int                       `json:"attempts"`
	NextAttemptUnixMS int64                     `json:"next_attempt_unix_ms"`
	Unavailable       bool                      `json:"unavailable"`
	Reservation       *ownerRecoveryReservation `json:"reservation,omitempty"`
	OwnerPending      *ownerRecoveryPending     `json:"owner_pending,omitempty"`
}
type ownerRecoveryState struct {
	file   *hostRecoveryState
	record ownerRecoveryRecord
	failed bool
}

func ownerRecoveryRejected() error { return errors.New("owner_recovery_state_rejected") }

// The physical transport and Coordinator epoch may change. The original
// signing process may not: a different owner/key must never reset this ledger.
func originalOwnerRecoveryScope(scope contract.OriginalOwnerScope, owner contract.OriginalOwnerIdentity) (string, error) {
	scopeHash, err := contract.OriginalOwnerScopeSHA256(scope)
	if err != nil {
		return "", err
	}
	identityHash, err := contract.OriginalOwnerIdentitySHA256(owner)
	if err != nil {
		return "", err
	}
	return hashText("original-owner-recovery-v2\x00" + scopeHash + "\x00" + identityHash), nil
}
func openOwnerRecovery(directory, scope string) (*ownerRecoveryState, error) {
	digest, err := hex.DecodeString(scope)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != scope {
		return nil, ownerRecoveryRejected()
	}
	file, err := lockHostRecovery(directory)
	if err != nil {
		return nil, err
	}
	authority, err := file.authoritySHA256()
	if err != nil {
		file.close()
		return nil, err
	}
	s := &ownerRecoveryState{file: file, record: ownerRecoveryRecord{Version: 2, ScopeSHA256: scope, AuthoritySHA256: authority}}
	body, err := file.readFile()
	if errors.Is(err, os.ErrNotExist) {
		err = s.commit(s.record)
	} else if err == nil {
		var record ownerRecoveryRecord
		if contract.DecodeOwnerRuntimeStrict(body, &record) != nil || record.ScopeSHA256 != scope || record.AuthoritySHA256 != authority || validateOwnerRecovery(record) != nil {
			err = ownerRecoveryRejected()
		} else {
			canonical, _ := json.Marshal(record)
			if !bytes.Equal(append(canonical, '\n'), body) {
				err = ownerRecoveryRejected()
			} else {
				s.record = record
				s.file.bytes = body
			}
		}
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *ownerRecoveryState) close() {
	if s.file != nil {
		s.file.close()
	}
}
func (s *ownerRecoveryState) reserveTransport(now time.Time, id string, revision uint64, old contract.OriginalOwnerTransport) (time.Time, error) {
	if s.record.OwnerPending != nil {
		return time.Time{}, ownerRecoveryRejected()
	}
	next, when, err := s.reservation(now, "transport_start")
	if err != nil {
		return time.Time{}, err
	}
	next.OwnerPending = &ownerRecoveryPending{StartSlot: next.Attempts, RequestID: id, ExpectedControlRevision: revision, OldHost: old, Phase: "reserved"}
	if err = s.commit(next); err != nil {
		return time.Time{}, err
	}
	return when, nil
}
func (s *ownerRecoveryState) beginTransportStart() error {
	if s.record.OwnerPending == nil || s.record.OwnerPending.Phase != "reserved" || s.record.Reservation == nil || s.record.Reservation.Kind != "transport_start" || s.record.Reservation.Phase != "reserved" {
		return ownerRecoveryRejected()
	}
	next := s.copyRecord()
	next.Reservation.Phase = "starting"
	next.OwnerPending.Phase = "starting"
	return s.commit(next)
}
func (s *ownerRecoveryState) recordTransportStarted(child contract.OriginalOwnerTransport) error {
	if s.record.OwnerPending == nil || s.record.OwnerPending.Phase != "starting" || s.record.Reservation == nil || s.record.Reservation.Kind != "transport_start" || s.record.Reservation.Phase != "starting" {
		return ownerRecoveryRejected()
	}
	next := s.copyRecord()
	next.OwnerPending.Phase = "started"
	next.OwnerPending.NewHost = &child
	next.Reservation = nil
	return s.commit(next)
}
func (s *ownerRecoveryState) markTransportUnknown() error {
	if s.record.OwnerPending == nil || (s.record.OwnerPending.Phase != "reserved" && s.record.OwnerPending.Phase != "starting") {
		return ownerRecoveryRejected()
	}
	next := s.copyRecord()
	next.OwnerPending.Phase = "unknown"
	next.Reservation = nil
	return s.commit(next)
}
func (s *ownerRecoveryState) ticketPending() (contract.OwnerTransportPending, error) {
	p := s.record.OwnerPending
	if s.failed || s.file == nil || s.file.lock == nil || s.record.Unavailable || p == nil || p.Phase != "started" || p.NewHost == nil {
		return contract.OwnerTransportPending{}, ownerRecoveryRejected()
	}
	if err := s.verifyCurrent(); err != nil {
		return contract.OwnerTransportPending{}, err
	}
	return contract.OwnerTransportPending{Version: 1, RecoverySlot: p.StartSlot, RequestID: p.RequestID, ExpectedControlRevision: p.ExpectedControlRevision, OldHost: p.OldHost, NewHost: *p.NewHost}, nil
}
func (s *ownerRecoveryState) reserveCoordinator(now time.Time) (time.Time, error) {
	if p := s.record.OwnerPending; p != nil && p.Phase != "started" {
		return time.Time{}, ownerRecoveryRejected()
	}
	next, when, err := s.reservation(now, "coordinator")
	if err != nil {
		return time.Time{}, err
	}
	if err = s.commit(next); err != nil {
		return time.Time{}, err
	}
	return when, nil
}
func (s *ownerRecoveryState) beginCoordinatorEnsure() error {
	if s.record.Reservation == nil || s.record.Reservation.Kind != "coordinator" || s.record.Reservation.Phase != "reserved" {
		return ownerRecoveryRejected()
	}
	next := s.copyRecord()
	next.Reservation.Phase = "starting"
	return s.commit(next)
}
func (s *ownerRecoveryState) finishCoordinatorEnsure() error {
	if s.record.Reservation == nil || s.record.Reservation.Kind != "coordinator" || s.record.Reservation.Phase != "starting" {
		return ownerRecoveryRejected()
	}
	next := s.copyRecord()
	next.Reservation = nil
	return s.commit(next)
}

func (s *ownerRecoveryState) copyRecord() ownerRecoveryRecord {
	r := s.record
	if r.Reservation != nil {
		copy := *r.Reservation
		r.Reservation = &copy
	}
	if r.OwnerPending != nil {
		copy := *r.OwnerPending
		r.OwnerPending = &copy
		if copy.NewHost != nil {
			child := *copy.NewHost
			r.OwnerPending.NewHost = &child
		}
	}
	return r
}

func validateOwnerRecovery(r ownerRecoveryRecord) error {
	if r.Version != 2 || len(r.ScopeSHA256) != 64 || len(r.AuthoritySHA256) != 64 || r.Attempts < 0 || r.Attempts > 5 || r.NextAttemptUnixMS < 0 || (r.Attempts == 0 && (r.NextAttemptUnixMS != 0 || r.Reservation != nil || r.OwnerPending != nil)) || (r.Attempts > 0 && r.NextAttemptUnixMS == 0) || (r.Unavailable && (r.Attempts != 5 || r.Reservation != nil)) {
		return ownerRecoveryRejected()
	}
	if p := r.Reservation; p != nil {
		if p.Slot < 1 || p.Slot != r.Attempts || (p.Kind != "coordinator" && p.Kind != "transport_start") || (p.Phase != "reserved" && p.Phase != "starting") {
			return ownerRecoveryRejected()
		}
		if p.Kind == "transport_start" && r.OwnerPending == nil {
			return ownerRecoveryRejected()
		}
	}
	if p := r.OwnerPending; p != nil {
		if p.StartSlot < 1 || p.StartSlot > r.Attempts || p.RequestID == "" || len(p.RequestID) > 256 || !utf8.ValidString(p.RequestID) || strings.ContainsRune(p.RequestID, 0) || p.ExpectedControlRevision == 0 || p.ExpectedControlRevision >= math.MaxInt64 || p.OldHost.Validate() != nil {
			return ownerRecoveryRejected()
		}
		switch p.Phase {
		case "reserved", "starting":
			if p.NewHost != nil || r.Reservation == nil || r.Reservation.Kind != "transport_start" || r.Reservation.Slot != p.StartSlot || r.Reservation.Phase != p.Phase {
				return ownerRecoveryRejected()
			}
		case "started":
			if p.NewHost == nil || p.NewHost.Validate() != nil || *p.NewHost == p.OldHost || (r.Reservation != nil && r.Reservation.Kind != "coordinator") {
				return ownerRecoveryRejected()
			}
		case "unknown":
			if p.NewHost != nil || r.Reservation != nil {
				return ownerRecoveryRejected()
			}
		default:
			return ownerRecoveryRejected()
		}
	}
	return nil
}

func (s *ownerRecoveryState) commit(next ownerRecoveryRecord) error {
	if s.failed || s.file == nil || s.file.lock == nil || next.ScopeSHA256 != s.record.ScopeSHA256 || next.AuthoritySHA256 != s.record.AuthoritySHA256 || validateOwnerRecovery(next) != nil {
		return ownerRecoveryRejected()
	}
	body, err := json.Marshal(next)
	if err != nil || len(body)+1 > 4096 {
		return ownerRecoveryRejected()
	}
	body = append(body, '\n')
	// An existing durable reservation needs only a validated read. Replaying it
	// must not depend on another stage allocation/rename/directory sync.
	if bytes.Equal(body, s.file.bytes) {
		return s.verifyCurrent()
	}
	if err = s.file.persistBytes(body); err != nil {
		s.failed = true
		return err
	}
	s.record = next
	return nil
}

func (s *ownerRecoveryState) verifyCurrent() error {
	if s.failed || s.file == nil || s.file.lock == nil {
		return ownerRecoveryRejected()
	}
	authority, err := s.file.authoritySHA256()
	if err != nil || authority != s.record.AuthoritySHA256 {
		s.failed = true
		return ownerRecoveryRejected()
	}
	body, err := s.file.readFile()
	if err != nil || !bytes.Equal(body, s.file.bytes) {
		s.failed = true
		return ownerRecoveryRejected()
	}
	return nil
}

func (s *ownerRecoveryState) reservation(now time.Time, kind string) (ownerRecoveryRecord, time.Time, error) {
	var zero ownerRecoveryRecord
	if s.failed || validateOwnerRecovery(s.record) != nil {
		return zero, time.Time{}, ownerRecoveryRejected()
	}
	ms := now.UnixMilli()
	if ms < 0 || ms > math.MaxInt64-16_000 {
		return zero, time.Time{}, errors.New("host_recovery_time_untrusted")
	}
	if s.record.Attempts > 0 {
		maxDelay := int64(1000) << (s.record.Attempts - 1)
		if s.record.NextAttemptUnixMS > ms+maxDelay {
			return zero, time.Time{}, errors.New("host_recovery_time_untrusted")
		}
		if s.record.Reservation == nil && s.record.NextAttemptUnixMS > ms {
			return zero, time.Time{}, errors.New("host_recovery_clock_regressed")
		}
	}
	if p := s.record.Reservation; p != nil {
		if p.Kind != kind || p.Phase != "reserved" {
			return zero, time.Time{}, ownerRecoveryRejected()
		}
		return s.copyRecord(), time.UnixMilli(s.record.NextAttemptUnixMS), nil
	}
	if s.record.Unavailable || s.record.Attempts >= 5 {
		next := s.copyRecord()
		next.Unavailable = true
		if err := s.commit(next); err != nil {
			return zero, time.Time{}, err
		}
		return zero, time.Time{}, errCoordinatorUnavailable
	}
	next := s.copyRecord()
	next.Attempts++
	next.NextAttemptUnixMS = ms + (int64(1000) << s.record.Attempts)
	next.Reservation = &ownerRecoveryReservation{Slot: next.Attempts, Kind: kind, Phase: "reserved"}
	return next, time.UnixMilli(next.NextAttemptUnixMS), nil
}
