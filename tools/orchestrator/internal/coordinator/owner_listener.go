package coordinator

import (
	"errors"
	"net"
	"os"
	"path/filepath"
)

// The private final-event listener belongs to Server, not the public Serve
// accept context. It has no route to controller, bridge or transport handlers.
func (s *Server) startOriginalOwnerListener() error {
	path := filepath.Join(s.stateDir, "owner.sock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("untrusted_owner_socket_path")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return err
	}
	s.ownerListener, s.ownerSocket = listener, path
	s.ownerAcceptDone = make(chan struct{})
	go s.acceptOriginalOwners()
	return nil
}

func (s *Server) acceptOriginalOwners() {
	defer close(s.ownerAcceptDone)
	for {
		conn, err := s.ownerListener.Accept()
		if err != nil {
			return
		}
		s.ownerRPCMu.Lock()
		if s.closed.Load() {
			s.ownerRPCMu.Unlock()
			_ = conn.Close()
			continue
		}
		select {
		case s.ownerAdmission <- struct{}{}:
			s.ownerRPCs[conn] = struct{}{}
			s.ownerRPCWG.Add(1)
			s.ownerRPCMu.Unlock()
			go s.handleOriginalOwnerRPC(conn)
		default:
			s.ownerRPCMu.Unlock()
			_ = conn.Close()
		}
	}
}
