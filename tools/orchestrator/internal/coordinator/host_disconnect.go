package coordinator

import (
	"context"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Lock order is dispatchMu -> send mu (released by send) -> Server.mu -> DB.
// Store calls do not acquire Server/session locks. Reader-exit Close happens
// before this lock chain so a blocked writer can release dispatchMu.
func (s *Server) disconnectHost(session *hostSession) bool {
	session.transportClosed.Store(true)
	// Close this reader's own transport before waiting for the dispatch lock.
	// An in-flight send holds that lock; Close must first unblock its I/O.
	// net.Conn permits concurrent Close/read/write. No failed state is read here.
	_ = session.conn.Close()
	session.dispatchMu.Lock()
	defer session.dispatchMu.Unlock()
	return s.disconnectHostLocked(session)
}

// The caller holds dispatchMu. Only the current session may change the durable
// Host status; MarkHostOffline also requires this coordinator epoch. Failure to
// persist offline is not proof that FIFO eligibility changed. The connection
// still stays failed, so stale/concurrent scans cannot claim or write again.
// Reservations remain untouched, including a grant whose send was uncertain.
func (s *Server) disconnectHostLocked(session *hostSession) bool {
	if session.failed {
		return false
	}
	session.failed = true
	session.transportClosed.Store(true)
	_ = session.conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts[session.id] != session {
		return false
	}
	if session.ownerPermit != nil {
		err := s.db.FenceOriginalOwnerTransport(context.Background(), session.hello, s.epoch)
		delete(s.hosts, session.id)
		return err == nil
	}
	err := s.db.MarkHostOffline(context.Background(), session.id, s.epoch)
	delete(s.hosts, session.id)
	return err == nil
}

// These two durable admission refusals do not indicate a broken transport.
// Keep the verified Host session available for events and owner decisions.
func dispatchBudgetRejected(err error) bool {
	return errors.Is(err, store.ErrAttemptLimit) || errors.Is(err, store.CodeError("budget_exhausted"))
}
