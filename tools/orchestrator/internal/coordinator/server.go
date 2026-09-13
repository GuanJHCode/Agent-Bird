package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/instance"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type TaskRequest struct {
	ID            string   `json:"id"`
	Dependencies  []string `json:"dependencies,omitempty"`
	MaxAttempts   int      `json:"max_attempts"`
	WorkRevision  int      `json:"work_revision,omitempty"`
	BudgetGroupID string   `json:"budget_group_id,omitempty"`
	MaxActiveMS   int64    `json:"max_active_ms,omitempty"`
}

type SubmitRequest struct {
	RunID            string        `json:"run_id"`
	ControllerThread string        `json:"controller_thread"`
	PlanRevision     int           `json:"plan_revision"`
	OriginContextID  string        `json:"origin_context_id"`
	OriginPID        int           `json:"origin_pid"`
	OriginBirth      string        `json:"origin_birth"`
	HostGeneration   string        `json:"host_generation"`
	HostExecutable   string        `json:"host_executable"`
	Tasks            []TaskRequest `json:"tasks"`
}

type SubmitResponse struct {
	Status      string `json:"status"`
	RunID       string `json:"run_id"`
	LaunchID    string `json:"launch_id"`
	LaunchToken string `json:"launch_token"`
}

type TaskControlRequest struct {
	TaskID           string `json:"task_id"`
	ControllerThread string `json:"controller_thread"`
	WorkRevision     int    `json:"work_revision,omitempty"`
}

type AckRequest struct {
	TaskID           string   `json:"task_id"`
	ControllerThread string   `json:"controller_thread"`
	EventIDs         []string `json:"event_ids"`
}

type CollectResponse struct {
	Status string           `json:"status"`
	Events []contract.Event `json:"events"`
}

type StatusResponse struct {
	Status string `json:"status"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type hostSession struct {
	id   string
	conn net.Conn
	mu   sync.Mutex
}

func (h *hostSession) send(message ipc.Envelope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return ipc.Write(h.conn, message)
}

type Server struct {
	stateDir string
	socket   string
	epoch    uint64
	lock     *instance.Lock
	db       *store.DB
	listener net.Listener
	mu       sync.Mutex
	hosts    map[string]*hostSession
	close    sync.Once
}

func NewServer(stateDir string) (*Server, error) {
	if err := instance.PrepareStateDir(stateDir); err != nil {
		return nil, err
	}
	lock, err := instance.Acquire(stateDir)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	socket := filepath.Join(stateDir, "coordinator.sock")
	if info, statErr := os.Lstat(socket); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			_ = db.Close()
			_ = lock.Close()
			return nil, errors.New("untrusted_socket_path")
		}
		if err = os.Remove(socket); err != nil {
			_ = db.Close()
			_ = lock.Close()
			return nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = db.Close()
		_ = lock.Close()
		return nil, statErr
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		_ = listener.Close()
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	return &Server{stateDir: stateDir, socket: socket, epoch: lock.Epoch(), lock: lock, db: db, listener: listener, hosts: make(map[string]*hostSession)}, nil
}

func (s *Server) SocketPath() string { return s.socket }
func (s *Server) Epoch() uint64      { return s.epoch }

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConnection(ctx, conn)
	}
}

// Shutdown asks each owning Host to stop its active tree and waits for durable
// exit events. Work that cannot be confirmed before ctx expires remains
// unknown and continues to occupy its reservation after restart.
func (s *Server) Shutdown(ctx context.Context) error {
	active, err := s.db.ActiveTasks(context.Background())
	if err != nil {
		return err
	}
	for _, task := range active {
		dispatch, stopErr := s.db.RequestStop(context.Background(), task.TaskID, task.WorkRevision, "coordinator_exit")
		if stopErr != nil || dispatch.HostID == "" {
			continue
		}
		s.mu.Lock()
		host := s.hosts[dispatch.HostID]
		s.mu.Unlock()
		if host != nil {
			_ = sendStop(host, s.epoch, dispatch.Command)
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err = s.db.ActiveTasks(context.Background())
		if err != nil {
			return err
		}
		if len(active) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			_ = s.db.MarkStoppingUnknown(context.Background())
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) Close() error {
	var result error
	s.close.Do(func() {
		s.mu.Lock()
		for _, host := range s.hosts {
			_ = host.conn.Close()
		}
		s.hosts = nil
		s.mu.Unlock()
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = err
		}
		_ = os.Remove(s.socket)
		if err := s.db.Close(); result == nil {
			result = err
		}
		if err := s.lock.Close(); result == nil {
			result = err
		}
	})
	return result
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	message, err := ipc.Read(conn)
	if err != nil {
		_ = conn.Close()
		return
	}
	if message.Kind == ipc.KindHostHello {
		s.handleHost(ctx, conn, message)
		return
	}
	defer conn.Close()
	s.handleControl(ctx, conn, message)
}

func (s *Server) handleControl(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	payload, err := s.control(ctx, message.Kind, message.Payload)
	responseKind := ipc.KindResponse
	if err != nil {
		responseKind = ipc.KindError
		payload, _ = json.Marshal(ErrorResponse{Error: errorCode(err)})
	}
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: responseKind, RequestID: message.RequestID, Epoch: s.epoch, Payload: payload})
}

func (s *Server) control(ctx context.Context, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	switch kind {
	case ipc.KindReady:
		return json.Marshal(map[string]any{"status": "ready", "epoch": s.epoch})
	case ipc.KindSubmit:
		var request SubmitRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		tasks := make([]store.TaskSpec, 0, len(request.Tasks))
		for _, task := range request.Tasks {
			tasks = append(tasks, store.TaskSpec{ID: task.ID, RunID: request.RunID, Dependencies: task.Dependencies, MaxAttempts: task.MaxAttempts, WorkRevision: task.WorkRevision, BudgetGroupID: task.BudgetGroupID, MaxActiveMS: task.MaxActiveMS})
		}
		receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: request.RunID, ControllerThread: request.ControllerThread, PlanRevision: request.PlanRevision, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: request.OriginContextID, HostGeneration: request.HostGeneration, Executable: request.HostExecutable}, Tasks: tasks})
		if err != nil {
			return nil, err
		}
		return json.Marshal(SubmitResponse{Status: "queued", RunID: request.RunID, LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken})
	case ipc.KindStatus:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread); err != nil {
			return nil, err
		}
		snapshot, err := s.db.TaskSnapshot(ctx, request.TaskID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(snapshot)
	case ipc.KindCollect:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread); err != nil {
			return nil, err
		}
		events, err := s.db.CollectPending(ctx, request.TaskID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(CollectResponse{Status: "pending", Events: events})
	case ipc.KindAck:
		var request AckRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread); err != nil {
			return nil, err
		}
		if err := s.db.AckEvents(ctx, request.TaskID, request.EventIDs); err != nil {
			return nil, err
		}
		return json.Marshal(StatusResponse{Status: "acknowledged"})
	case ipc.KindResume:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread); err != nil {
			return nil, err
		}
		if err := s.db.QueueResume(ctx, request.TaskID, request.WorkRevision); err != nil {
			return nil, err
		}
		s.dispatchAll()
		return json.Marshal(StatusResponse{Status: "resume_queued"})
	case ipc.KindStopTask:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread); err != nil {
			return nil, err
		}
		dispatch, err := s.db.RequestStop(ctx, request.TaskID, request.WorkRevision, "controller_stop")
		if err != nil {
			return nil, err
		}
		if dispatch.HostID == "" {
			return json.Marshal(StatusResponse{Status: "cancelled"})
		}
		s.mu.Lock()
		host := s.hosts[dispatch.HostID]
		s.mu.Unlock()
		if host == nil {
			return nil, errors.New("host_unavailable")
		}
		if err = sendStop(host, s.epoch, dispatch.Command); err != nil {
			return nil, err
		}
		return json.Marshal(StatusResponse{Status: "stopping"})
	default:
		return nil, errors.New("unsupported_request")
	}
}

func (s *Server) handleHost(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	var hello contract.HostHello
	if err := strictJSON(message.Payload, &hello); err != nil {
		_ = conn.Close()
		return
	}
	hostID, err := s.db.RegisterHost(ctx, hello, s.epoch)
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		_ = conn.Close()
		return
	}
	session := &hostSession{id: hostID, conn: conn}
	s.mu.Lock()
	s.hosts[hostID] = session
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.hosts[hostID] == session {
			delete(s.hosts, hostID)
		}
		s.mu.Unlock()
		_ = conn.Close()
	}()
	ready, _ := json.Marshal(contract.HostReady{HostID: hostID, CoordinatorEpoch: s.epoch})
	if err = session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: message.RequestID, Epoch: s.epoch, Payload: ready}); err != nil {
		return
	}
	if err = s.dispatch(session, true); err != nil {
		return
	}
	for {
		incoming, readErr := ipc.Read(conn)
		if readErr != nil || incoming.Epoch != s.epoch || incoming.Kind != ipc.KindEvent {
			return
		}
		var event contract.Event
		if strictJSON(incoming.Payload, &event) != nil || event.ProducerID != hostID {
			return
		}
		ack, commitErr := s.db.CommitHostEvent(ctx, event)
		if commitErr != nil {
			_ = sendError(conn, incoming.RequestID, s.epoch, commitErr)
			continue
		}
		body, _ := json.Marshal(ack)
		if err = session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: incoming.RequestID, Epoch: s.epoch, Payload: body}); err != nil {
			return
		}
		if err = s.dispatch(session, false); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(session *hostSession, includePending bool) error {
	if includePending {
		pending, err := s.db.PendingLaunches(context.Background(), session.id, s.epoch)
		if err != nil {
			return err
		}
		for _, command := range pending {
			if err = sendLaunch(session, s.epoch, command); err != nil {
				return err
			}
		}
	}
	for {
		command, err := s.db.ClaimReady(context.Background(), session.id, s.epoch, 2)
		if errors.Is(err, store.ErrNoReady) || errors.Is(err, store.ErrNoSlot) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = sendLaunch(session, s.epoch, command); err != nil {
			return err
		}
	}
}

func (s *Server) dispatchAll() {
	s.mu.Lock()
	hosts := make([]*hostSession, 0, len(s.hosts))
	for _, host := range s.hosts {
		hosts = append(hosts, host)
	}
	s.mu.Unlock()
	for _, host := range hosts {
		_ = s.dispatch(host, false)
	}
}

func sendLaunch(session *hostSession, epoch uint64, command contract.LaunchCommand) error {
	body, _ := json.Marshal(command)
	return session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: command.CommandID, Epoch: epoch, Payload: body})
}

func sendStop(session *hostSession, epoch uint64, command contract.StopCommand) error {
	body, _ := json.Marshal(command)
	return session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindStop, RequestID: command.CommandID, Epoch: epoch, Payload: body})
}

func sendError(conn net.Conn, requestID string, epoch uint64, err error) error {
	body, _ := json.Marshal(ErrorResponse{Error: errorCode(err)})
	return ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindError, RequestID: requestID, Epoch: epoch, Payload: body})
}

func strictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
