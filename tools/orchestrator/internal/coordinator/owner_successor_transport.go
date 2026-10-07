package coordinator

import (
	"encoding/hex"
	"net"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Registration serialization is held. This map does not grant reconciliation,
// worker dispatch or physical Host publication; only Prepare may promote it.
func (s *Server) registerSuccessorOwnerTransport(conn net.Conn, requestID string, hello contract.HostHello, admission store.SuccessorOwnerAdmission) (*hostSession, contract.OwnerHostPending, error) {
	var zero contract.OwnerHostPending
	id := admission.Scope.ProducerID
	session := &hostSession{id: id, hello: hello, conn: conn, helloRequestID: requestID, supervised: true, successorAuthorizationID: admission.AuthorizationID}
	s.mu.Lock()
	if s.closed.Load() || s.terminating.Load() || s.ownerTransports[id] != nil || s.hosts[id] != nil {
		s.mu.Unlock()
		return nil, zero, ownerRejected()
	}
	old := s.successorTransports[id]
	if old != nil && (old.hello.PID != hello.PID || old.hello.Birth != hello.Birth || old.successorAuthorizationID != admission.AuthorizationID) {
		s.mu.Unlock()
		return nil, zero, store.ErrConflict
	}
	s.successorTransports[id] = session
	s.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	return session, contract.OwnerHostPending{Version: 1, HostID: id, OwnerID: admission.Scope.OwnerID, CoordinatorEpoch: s.epoch, CoordinatorPublicKey: hex.EncodeToString(s.ownerPublicKey())}, nil
}

func (s *Server) promoteSuccessorOwnerTransport(session *hostSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() || s.terminating.Load() {
		return ownerRejected()
	}
	if s.ownerTransports[session.id] == session {
		return nil
	}
	if s.successorTransports[session.id] != session || s.ownerTransports[session.id] != nil || s.hosts[session.id] != nil {
		return ownerRejected()
	}
	delete(s.successorTransports, session.id)
	s.ownerTransports[session.id] = session
	return nil
}
