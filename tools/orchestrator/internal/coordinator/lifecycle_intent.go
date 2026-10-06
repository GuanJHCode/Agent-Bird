package coordinator

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const restartIntentName = "coordinator-restart-intent.json"
const restartIntentLimit = 1024 * 1024

type restartIntent struct {
	Version         int      `json:"version"`
	Epoch           uint64   `json:"epoch"`
	Blocked         bool     `json:"blocked"`
	TerminatedHosts []string `json:"terminated_hosts"`
}

func readRestartIntent(state string) (restartIntent, error) {
	path := filepath.Join(state, restartIntentName)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return restartIntent{Version: 1, TerminatedHosts: []string{}}, nil
	}
	if err != nil {
		return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return restartIntent{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() < 1 || info.Size() > restartIntentLimit {
		return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(f, restartIntentLimit+1))
	if err != nil || int64(len(body)) != info.Size() {
		return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
	}
	var value restartIntent
	if json.Unmarshal(body, &value) != nil || value.Version != 1 || value.Epoch == 0 || value.TerminatedHosts == nil {
		return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
	}
	for i, hash := range value.TerminatedHosts {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 || (i > 0 && value.TerminatedHosts[i-1] >= hash) {
			return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
		}
	}
	canonical, _ := json.Marshal(value)
	if !bytes.Equal(append(canonical, '\n'), body) {
		return restartIntent{}, store.CodeError("coordinator_restart_intent_unsafe")
	}
	return value, nil
}

// CheckRecoveryAllowed is read-only. The recovery server rechecks Blocked
// while holding the coordinator instance lock before touching the DB/socket.
func CheckRecoveryAllowed(state string, hello contract.HostHello) error {
	value, err := readRestartIntent(state)
	if err != nil {
		return err
	}
	identity := contract.HostIdentitySHA256(hello)
	index := sort.SearchStrings(value.TerminatedHosts, identity)
	if value.Blocked || (index < len(value.TerminatedHosts) && value.TerminatedHosts[index] == identity) {
		return store.CodeError("coordinator_shutdown")
	}
	return nil
}

func writeRestartIntent(state string, value restartIntent) error {
	// Validate an existing record before replacing it. All writers hold the
	// instance lock and lifecycleMu; no repair of untrusted files is attempted.
	if _, err := readRestartIntent(state); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > restartIntentLimit {
		return store.CodeError("coordinator_restart_intent_full")
	}
	stage, err := os.CreateTemp(state, ".restart-intent-*.new")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	if _, err = stage.Write(body); err == nil {
		err = stage.Sync()
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(stage.Name(), filepath.Join(state, restartIntentName))
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(state)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func clearGlobalRestartBlock(state string, epoch uint64) error {
	value, err := readRestartIntent(state)
	if err != nil {
		return err
	}
	if !value.Blocked {
		return nil
	}
	value.Blocked = false
	value.Epoch = epoch
	return writeRestartIntent(state, value)
}

func (s *Server) beginLifecycleExit() error {
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed.Load() || s.terminating.Load() {
		return nil
	}
	identities, err := s.db.HostLifecycleIdentities(context.Background())
	if err != nil {
		return err
	}
	value, err := readRestartIntent(s.stateDir)
	if err != nil {
		return err
	}
	set := make(map[string]bool, len(value.TerminatedHosts)+len(identities))
	for _, hash := range value.TerminatedHosts {
		set[hash] = true
	}
	for _, hello := range identities {
		set[contract.HostIdentitySHA256(hello)] = true
	}
	value.TerminatedHosts = make([]string, 0, len(set))
	for hash := range set {
		value.TerminatedHosts = append(value.TerminatedHosts, hash)
	}
	sort.Strings(value.TerminatedHosts)
	value.Version, value.Epoch, value.Blocked = 1, s.epoch, true
	if err := writeRestartIntent(s.stateDir, value); err != nil {
		return err
	}
	s.terminating.Store(true)
	return nil
}

func sendHostDisposition(session *hostSession, epoch uint64, reason string) error {
	value := contract.NewHostDisposition(session.hello, epoch, reason)
	body, _ := json.Marshal(value)
	// A peer that stopped reading cannot prevent a bounded lifecycle transition.
	_ = session.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer session.conn.SetWriteDeadline(time.Time{})
	return session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostDisposition, RequestID: session.id + ":disposition", Epoch: epoch, Payload: body})
}

func (s *Server) notifyLifecycleExit() {
	s.mu.Lock()
	sessions := make([]*hostSession, 0, len(s.hosts))
	for _, session := range s.hosts {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		_ = sendHostDisposition(session, s.epoch, "coordinator_exit")
	}
}
