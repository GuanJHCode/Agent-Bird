package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type TaskRequest struct {
	ID                     string            `json:"id"`
	Dependencies           []string          `json:"dependencies,omitempty"`
	MaxAttempts            int               `json:"max_attempts"`
	WorkRevision           int               `json:"work_revision,omitempty"`
	BudgetGroupID          string            `json:"budget_group_id,omitempty"`
	MaxActiveMS            int64             `json:"max_active_ms,omitempty"`
	AdapterPayload         json.RawMessage   `json:"adapter"`
	Fallbacks              []json.RawMessage `json:"fallbacks,omitempty"`
	CompletionPolicy       string            `json:"completion_policy,omitempty"`
	ExpectedArtifactSHA256 string            `json:"expected_artifact_sha256,omitempty"`
}

type SubmitRequest struct {
	OwnerMode        string        `json:"owner_mode,omitempty"`
	DeliveryMode     string        `json:"delivery_mode,omitempty"`
	RunID            string        `json:"run_id"`
	ControllerThread string        `json:"controller_thread"`
	PlanRevision     int           `json:"plan_revision"`
	OriginContextID  string        `json:"origin_context_id"`
	OriginPID        int           `json:"origin_pid"`
	OriginBirth      string        `json:"origin_birth"`
	HostGeneration   string        `json:"host_generation"`
	OwnerCapability  string        `json:"owner_capability,omitempty"`
	HostExecutable   string        `json:"host_executable"`
	Tasks            []TaskRequest `json:"tasks"`
	LaunchID         string        `json:"launch_id,omitempty"`
	LaunchToken      string        `json:"launch_token,omitempty"`
	ControlToken     string        `json:"control_token,omitempty"`
}

type SubmitResponse struct {
	Status       string `json:"status"`
	RunID        string `json:"run_id"`
	LaunchID     string `json:"launch_id"`
	LaunchToken  string `json:"launch_token"`
	ControlToken string `json:"control_token"`
}

type TaskControlRequest struct {
	TaskID           string `json:"task_id"`
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	WorkRevision     int    `json:"work_revision,omitempty"`
}

type CollectRequest struct {
	TaskControlRequest
	Cursor             string `json:"cursor,omitempty"`
	IncludeDiagnostics bool   `json:"include_diagnostics,omitempty"`
}

type WaitEventsRequest struct {
	TaskControlRequest
	Cursor    string `json:"cursor,omitempty"`
	TimeoutMS int    `json:"timeout_ms"`
}

type AckRequest struct {
	CollectionProofSHA256 string              `json:"collection_proof_sha256,omitempty"`
	TaskID                string              `json:"task_id"`
	ControllerThread      string              `json:"controller_thread"`
	ControlToken          string              `json:"control_token"`
	DeliveryID            string              `json:"delivery_id"`
	HistoryProofSHA256    string              `json:"history_proof_sha256"`
	Decisions             []store.AckDecision `json:"decisions"`
}

type RetryRequest struct {
	TaskControlRequest
	EventID         string `json:"event_id"`
	EventRevision   int64  `json:"event_revision"`
	EventHash       string `json:"event_hash"`
	ActionSlot      string `json:"action_slot"`
	SegmentID       string `json:"segment_id"`
	NextAttemptNo   int    `json:"next_attempt_no"`
	UseNextFallback bool   `json:"use_next_fallback,omitempty"`
	CommandID       string `json:"command_id"`
}

type ReworkRequest struct {
	store.ReworkSpec
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
}

type AnswerRequest struct {
	TaskID           string `json:"task_id"`
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	WorkRevision     int    `json:"work_revision"`
	QuestionID       string `json:"question_id"`
	QuestionRevision int    `json:"question_revision"`
	Answer           string `json:"answer"`
}

type ReviewRequest struct {
	TaskControlRequest
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	Decision      string `json:"decision"`
	CommandID     string `json:"command_id"`
}

type ReportEventRequest struct {
	CapabilityID string          `json:"capability_id"`
	Token        string          `json:"token"`
	EventID      string          `json:"event_id"`
	Sequence     int64           `json:"sequence"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
}

type RebindOwnerRequest struct {
	OwnerMode             string `json:"owner_mode,omitempty"`
	OwnerCapability       string `json:"owner_capability,omitempty"`
	RunID                 string `json:"run_id"`
	ControllerThread      string `json:"controller_thread"`
	ControlToken          string `json:"control_token"`
	OriginContextID       string `json:"origin_context_id"`
	OriginPID             int    `json:"origin_pid"`
	OriginBirth           string `json:"origin_birth"`
	HostGeneration        string `json:"host_generation"`
	AttachmentProofSHA256 string `json:"attachment_proof_sha256"`
}

type CollectResponse struct {
	DeliveryID            string           `json:"delivery_id,omitempty"`
	CollectionProofSHA256 string           `json:"collection_proof_sha256,omitempty"`
	Version               int              `json:"version"`
	Status                string           `json:"status"`
	Events                []contract.Event `json:"events"`
	NextCursor            string           `json:"next_cursor,omitempty"`
}

type StatusResponse struct {
	Status string `json:"status"`
}

type HostStatusRequest struct {
	LaunchID    string `json:"launch_id"`
	LaunchToken string `json:"launch_token"`
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
	stateDir           string
	socket             string
	epoch              uint64
	lock               *instance.Lock
	db                 *store.DB
	listener           net.Listener
	mu                 sync.Mutex
	hostRegistrationMu sync.Mutex
	hosts              map[string]*hostSession
	inFlight           int
	close              sync.Once
	idleTimeout        time.Duration
	idleExit           atomic.Bool
	eventMu            sync.Mutex
	eventSignal        chan struct{}
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
	if err = loadStartupLimits(stateDir, db); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err = db.BeginCoordinatorEpoch(context.Background(), lock.Epoch()); err != nil {
		_ = db.Close()
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
	server := &Server{stateDir: stateDir, socket: socket, epoch: lock.Epoch(), lock: lock, db: db, listener: listener, hosts: make(map[string]*hostSession), idleTimeout: 30 * time.Second, eventSignal: make(chan struct{})}
	server.releaseTaskPins()
	return server, nil
}

func (s *Server) SocketPath() string { return s.socket }
func (s *Server) Epoch() uint64      { return s.epoch }

func (s *Server) currentEventSignal() <-chan struct{} {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	return s.eventSignal
}

func (s *Server) notifyActionableEvent() {
	s.eventMu.Lock()
	close(s.eventSignal)
	s.eventSignal = make(chan struct{})
	s.eventMu.Unlock()
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	go s.monitorIdle(ctx)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.idleExit.Load() {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.idleExit.Load() {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.inFlight++
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				s.inFlight--
				s.mu.Unlock()
			}()
			s.handleConnection(ctx, conn)
		}()
	}
}

func (s *Server) monitorIdle(ctx context.Context) {
	interval := 250 * time.Millisecond
	if s.idleTimeout < interval {
		interval = s.idleTimeout / 4
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var since time.Time
	for {
		s.mu.Lock()
		busy := s.inFlight != 0
		idle, err := false, error(nil)
		if !busy {
			idle, err = s.db.CompletelyIdle(context.Background())
		}
		if err == nil && idle {
			if since.IsZero() {
				since = time.Now()
			} else if time.Since(since) >= s.idleTimeout {
				s.idleExit.Store(true)
				_ = s.listener.Close()
				s.mu.Unlock()
				return
			}
		} else {
			since = time.Time{}
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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
	if message.Kind == ipc.KindHostProgress {
		s.hostProgress(ctx, conn, message)
		return
	}
	controlCtx := ctx
	if message.Kind == ipc.KindWaitEvents {
		var cancel context.CancelFunc
		controlCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			var probe [1]byte
			_, _ = conn.Read(probe[:])
			cancel()
		}()
	}
	s.handleControl(controlCtx, conn, message)
}

func (s *Server) handleControl(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	var payload json.RawMessage
	var err error
	switch message.Kind {
	case ipc.KindProviderControl, ipc.KindRecoverHost, ipc.KindOwnerBind, ipc.KindSubmit, ipc.KindRebindOwner, ipc.KindResume, ipc.KindRetry, ipc.KindRework, ipc.KindAccept, ipc.KindAnswer, ipc.KindStopTask, ipc.KindAck, ipc.KindCollect, ipc.KindWaitEvents, ipc.KindSummary, ipc.KindStatus:
		ctx, err = s.authorizePeer(ctx, conn)
	}
	if err == nil {
		switch message.Kind {
		case ipc.KindProviderControl, ipc.KindRecoverHost, ipc.KindRebindOwner, ipc.KindResume, ipc.KindRetry, ipc.KindRework, ipc.KindAccept, ipc.KindAnswer:
			ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
			err = s.db.ValidateDispatchPeer(ctx, ancestry)
		}
	}
	if err == nil {
		payload, err = s.control(ctx, message.Kind, message.Payload)
	}
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
		return json.Marshal(map[string]any{"status": "ready", "epoch": s.epoch, "capabilities": []string{"interruption_feedback_v1", "grok_readonly_v1", "session_provider_lifecycle_v1"}})
	case ipc.KindOwnerBind:
		return s.bindLocalOwner(ctx, raw)
	case ipc.KindProviderControl:
		return s.providerControl(ctx, raw)
	case ipc.KindSubmit:
		var request SubmitRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if request.OwnerMode == "local" {
			g, err := s.localOwner(ctx, request.OwnerCapability)
			if err != nil {
				return nil, err
			}
			if g.ControllerThread != request.ControllerThread || g.OriginContextID != request.OriginContextID || g.OriginPID != request.OriginPID || g.OriginBirth != request.OriginBirth || g.HostGeneration != request.HostGeneration {
				return nil, store.CodeError("owner_capability_mismatch")
			}
		} else if request.OwnerMode != "" && request.OwnerMode != "native" {
			return nil, store.CodeError("owner_mode_invalid")
		} else if err := s.db.ValidateOwnerRegistration(ctx); err != nil {
			return nil, err
		}
		tasks := make([]store.TaskSpec, 0, len(request.Tasks))
		for _, task := range request.Tasks {
			policy := task.CompletionPolicy
			if policy == "" {
				policy = "owner_review"
			}
			tasks = append(tasks, store.TaskSpec{ID: task.ID, RunID: request.RunID, Dependencies: task.Dependencies, MaxAttempts: task.MaxAttempts, WorkRevision: task.WorkRevision, BudgetGroupID: task.BudgetGroupID, MaxActiveMS: task.MaxActiveMS, AdapterPayload: task.AdapterPayload, FallbackPayloads: task.Fallbacks, CompletionPolicy: policy, ExpectedArtifactSHA256: task.ExpectedArtifactSHA256})
		}
		receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: request.DeliveryMode, ID: request.RunID, ControllerThread: request.ControllerThread, PlanRevision: request.PlanRevision, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: request.OriginContextID, HostGeneration: request.HostGeneration, Executable: request.HostExecutable}, Tasks: tasks, LaunchID: request.LaunchID, LaunchToken: request.LaunchToken, ControlToken: request.ControlToken})
		if err != nil {
			return nil, err
		}
		return json.Marshal(SubmitResponse{Status: "queued", RunID: request.RunID, LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, ControlToken: receipt.ControlToken})
	case ipc.KindRecoverHost:
		return s.inspectHostRecovery(ctx, raw)
	case ipc.KindHostStatus:
		var request HostStatusRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		status, err := s.db.HostLaunchStatus(ctx, request.LaunchID, request.LaunchToken)
		if err != nil {
			return nil, err
		}
		return json.Marshal(StatusResponse{Status: status})
	case ipc.KindSummary:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		summary, err := s.db.SummarizeRun(ctx, request.TaskID, request.ControllerThread, request.ControlToken)
		if err != nil {
			return nil, err
		}
		return json.Marshal(summary)
	case ipc.KindStatus:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		snapshot, err := s.db.TaskSnapshot(ctx, request.TaskID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(snapshot)
	case ipc.KindCollect:
		var request CollectRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		page, err := s.db.CollectPending(ctx, request.TaskID, request.Cursor, 0, request.IncludeDiagnostics)
		if err != nil {
			return nil, err
		}
		return s.collectionResponse(ctx, request.TaskID, "pending", page)
	case ipc.KindWaitEvents:
		var request WaitEventsRequest
		if err := strictJSON(raw, &request); err != nil || request.TimeoutMS < 1 || request.TimeoutMS > 30_000 {
			return nil, store.CodeError("invalid_wait_events")
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		timer := time.NewTimer(time.Duration(request.TimeoutMS) * time.Millisecond)
		defer timer.Stop()
		for {
			signal := s.currentEventSignal()
			page, err := s.db.CollectPending(ctx, request.TaskID, request.Cursor, 8, false)
			if err != nil {
				return nil, err
			}
			if len(page.Events) != 0 {
				return s.collectionResponse(ctx, request.TaskID, "events", page)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				return json.Marshal(CollectResponse{Version: 1, Status: "timeout", Events: []contract.Event{}})
			case <-signal:
			}
		}
	case ipc.KindAck:
		var request AckRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		if err := s.db.AckDelivery(ctx, request.TaskID, request.DeliveryID, request.HistoryProofSHA256, request.CollectionProofSHA256, request.Decisions); err != nil {
			return nil, err
		}
		s.retireCompletedHosts()
		s.releaseTaskPins()
		return json.Marshal(map[string]any{"version": 1, "status": "acknowledged", "delivery_id": request.DeliveryID})
	case ipc.KindResume:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		if err := s.db.QueueResume(ctx, request.TaskID, request.WorkRevision); err != nil {
			return nil, err
		}
		s.dispatchAll()
		return json.Marshal(StatusResponse{Status: "resume_queued"})
	case ipc.KindRetry:
		var request RetryRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		receipt, err := s.db.QueueRetry(ctx, store.RetrySpec{TaskID: request.TaskID, WorkRevision: request.WorkRevision, EventID: request.EventID, EventRevision: request.EventRevision, EventHash: request.EventHash, ActionSlot: request.ActionSlot, SegmentID: request.SegmentID, NextAttemptNo: request.NextAttemptNo, UseNextFallback: request.UseNextFallback, CommandID: request.CommandID})
		if err != nil {
			return nil, err
		}
		s.dispatchAll()
		return json.Marshal(receipt)
	case ipc.KindRework:
		var request ReworkRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		receipt, err := s.db.QueueRework(ctx, request.ReworkSpec)
		if err != nil {
			return nil, err
		}
		s.dispatchAll()
		return json.Marshal(receipt)
	case ipc.KindAccept:
		var request ReviewRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		status, err := s.db.ReviewResult(ctx, store.ReviewSpec{TaskID: request.TaskID, WorkRevision: request.WorkRevision, EventID: request.EventID, EventRevision: request.EventRevision, EventHash: request.EventHash, ActionSlot: request.ActionSlot, Decision: request.Decision, CommandID: request.CommandID})
		if err != nil {
			return nil, err
		}
		s.dispatchAll()
		s.releaseTaskPins()
		return json.Marshal(StatusResponse{Status: status})
	case ipc.KindAnswer:
		var request AnswerRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		status, err := s.db.AnswerQuestion(ctx, store.AnswerSpec{TaskID: request.TaskID, WorkRevision: request.WorkRevision, QuestionID: request.QuestionID, QuestionRevision: request.QuestionRevision, Answer: request.Answer})
		if err != nil {
			return nil, err
		}
		s.dispatchAll()
		return json.Marshal(StatusResponse{Status: status})
	case ipc.KindReportEvent:
		var request ReportEventRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		ack, err := s.db.CommitReportEvent(ctx, store.ReportEventSpec{CapabilityID: request.CapabilityID, Token: request.Token, EventID: request.EventID, Sequence: request.Sequence, Kind: request.Kind, Payload: request.Payload})
		if err != nil {
			return nil, err
		}
		if request.Kind != "progress" {
			s.notifyActionableEvent()
		}
		if request.Kind == "question" {
			s.dispatchPendingStops()
		}
		return json.Marshal(ack)
	case ipc.KindRebindOwner:
		var request RebindOwnerRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if request.OwnerMode == "local" {
			g, err := s.localOwner(ctx, request.OwnerCapability)
			if err != nil {
				return nil, err
			}
			if g.ControllerThread != request.ControllerThread || g.OriginPID != request.OriginPID || g.OriginBirth != request.OriginBirth || g.HostGeneration != request.HostGeneration {
				return nil, store.ErrConflict
			}
			sum := sha256.Sum256([]byte("local-owner-rebind-v1\x00" + g.ID + "\x00" + g.OriginBirth))
			request.AttachmentProofSHA256 = hex.EncodeToString(sum[:])
		} else if request.OwnerMode != "" && request.OwnerMode != "native" {
			return nil, store.CodeError("owner_mode_invalid")
		}
		binding, err := s.db.ValidateRunOwner(ctx, request.RunID, request.ControllerThread, request.ControlToken)
		if err != nil || binding.OriginContextID != request.OriginContextID {
			return nil, store.ErrConflict
		}
		if birth, birthErr := process.Birth(binding.OriginPID); birthErr == nil && birth == binding.OriginBirth {
			return nil, store.ErrConflict
		}
		if birth, birthErr := process.Birth(request.OriginPID); birthErr != nil || birth != request.OriginBirth {
			return nil, store.ErrConflict
		}
		if err = s.db.RebindOwner(ctx, store.RebindOwnerSpec{RunID: request.RunID, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth, HostGeneration: request.HostGeneration, AttachmentProofSHA256: request.AttachmentProofSHA256}); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"version": 1, "status": "owner_rebound", "run_id": request.RunID, "origin_context_id": request.OriginContextID, "host_generation": request.HostGeneration})
	case ipc.KindStopTask:
		var request TaskControlRequest
		if err := strictJSON(raw, &request); err != nil {
			return nil, err
		}
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		dispatch, err := s.db.RequestStop(ctx, request.TaskID, request.WorkRevision, "controller_stop")
		if err != nil {
			return nil, err
		}
		if dispatch.HostID == "" {
			s.releaseTaskPins()
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

func (s *Server) releaseTaskPins() {
	tasks, err := s.db.ReleasableTasks(context.Background())
	if err != nil {
		return
	}
	for _, task := range tasks {
		_ = install.UnpinRunningVersion(task.Executable, task.TaskID)
	}
}

// authorizeHostHello authenticates the peer and persisted owner before any
// registration or rebind writes. A daemon need not remain an owner descendant.
func (s *Server) authorizeHostHello(ctx context.Context, conn net.Conn, hello contract.HostHello) error {
	owner, err := s.db.ValidateHostHello(ctx, hello)
	if err != nil {
		return store.ErrHostRejected
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil || pid != hello.PID {
		return store.ErrHostRejected
	}
	ancestry, err := process.Ancestry(pid)
	if err != nil || ancestry[pid] != hello.Birth {
		return store.ErrHostRejected
	}
	// A registered Host may reconnect as itself. Keep every other Host or
	// Worker ancestor excluded, including a new peer using a stolen launch token.
	workerAncestry := make(map[int]string, len(ancestry))
	for ancestor, birth := range ancestry {
		workerAncestry[ancestor] = birth
	}
	binding, bindingErr := s.db.HostBinding(ctx, hello.LaunchID)
	if bindingErr != nil && !errors.Is(bindingErr, store.ErrNotFound) {
		return store.ErrHostRejected
	}
	if bindingErr == nil && binding.PID == pid && binding.Birth == hello.Birth {
		delete(workerAncestry, pid)
	}
	worker, err := s.db.IsWorkerAncestry(ctx, workerAncestry)
	if err != nil || worker {
		return store.ErrHostRejected
	}
	executable, err := process.ExecutablePath(pid)
	if err != nil {
		return store.ErrHostRejected
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || executable != hello.Executable {
		return store.ErrHostRejected
	}
	if birth, err := process.Birth(pid); err != nil || birth != hello.Birth {
		return store.ErrHostRejected
	}
	if birth, err := process.Birth(owner.OriginPID); err != nil || birth != owner.OriginBirth {
		return store.ErrHostRejected
	}
	return nil
}

func (s *Server) handleHost(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	var hello contract.HostHello
	if err := strictJSON(message.Payload, &hello); err != nil {
		_ = conn.Close()
		return
	}
	if err := s.authorizeHostHello(ctx, conn, hello); err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, store.ErrHostRejected)
		_ = conn.Close()
		return
	}
	hostID, err := s.registerHost(ctx, hello)
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		_ = conn.Close()
		return
	}
	if err = s.db.BeginHostReconcile(context.Background(), hostID); err != nil {
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
		current := false
		if s.hosts[hostID] == session {
			delete(s.hosts, hostID)
			current = true
		}
		s.mu.Unlock()
		if current {
			_ = s.db.MarkHostOffline(context.Background(), hostID, s.epoch)
		}
		_ = conn.Close()
	}()
	ready, _ := json.Marshal(contract.HostReady{HostID: hostID, CoordinatorEpoch: s.epoch})
	if err = session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: message.RequestID, Epoch: s.epoch, Payload: ready}); err != nil {
		return
	}
	for {
		reconcileMessage, readErr := ipc.Read(conn)
		if readErr != nil || reconcileMessage.Epoch != s.epoch {
			return
		}
		if reconcileMessage.Kind == ipc.KindEvent {
			_, commitErr := s.commitHostEvent(session, reconcileMessage)
			if commitErr != nil {
				return
			}
			continue
		}
		if reconcileMessage.Kind == ipc.KindReportCapability {
			if s.commitReportCapability(session, reconcileMessage) != nil {
				return
			}
			continue
		}
		if reconcileMessage.Kind != ipc.KindHostReconciled {
			return
		}
		var reconciled contract.HostReconciled
		if strictJSON(reconcileMessage.Payload, &reconciled) != nil || reconciled.ProducerID != hostID {
			return
		}
		break
	}
	if err = s.db.FinishHostReconcile(context.Background(), hostID); err != nil {
		return
	}
	if err = s.dispatch(session, true); err != nil {
		return
	}
	for {
		incoming, readErr := ipc.Read(conn)
		if readErr != nil || incoming.Epoch != s.epoch {
			return
		}
		if incoming.Kind == ipc.KindReportCapability {
			if s.commitReportCapability(session, incoming) != nil {
				return
			}
			continue
		}
		if incoming.Kind != ipc.KindEvent {
			return
		}
		committed, commitErr := s.commitHostEvent(session, incoming)
		if commitErr != nil {
			return
		}
		if !committed {
			continue
		}
		s.dispatchPendingStops()
		s.retireCompletedHosts()
		s.mu.Lock()
		_, stillRegistered := s.hosts[session.id]
		s.mu.Unlock()
		if !stillRegistered {
			return
		}
		if err = s.dispatch(session, false); err != nil {
			return
		}
	}
}

func (s *Server) dispatchPendingStops() {
	s.mu.Lock()
	hosts := make([]*hostSession, 0, len(s.hosts))
	for _, session := range s.hosts {
		hosts = append(hosts, session)
	}
	s.mu.Unlock()
	for _, session := range hosts {
		pending, err := s.db.PendingStops(context.Background(), session.id)
		if err != nil {
			continue
		}
		for _, dispatch := range pending {
			_ = sendStop(session, s.epoch, dispatch.Command)
		}
	}
}

func (s *Server) commitReportCapability(session *hostSession, incoming ipc.Envelope) error {
	var registration contract.ReportCapabilityRegistration
	if strictJSON(incoming.Payload, &registration) != nil || registration.ProducerID != session.id {
		return errors.New("invalid_report_capability")
	}
	ack, err := s.db.RegisterReportCapability(context.Background(), registration)
	if err != nil {
		_ = sendError(session.conn, incoming.RequestID, s.epoch, err)
		return nil
	}
	body, _ := json.Marshal(ack)
	return session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: incoming.RequestID, Epoch: s.epoch, Payload: body})
}

func (s *Server) commitHostEvent(session *hostSession, incoming ipc.Envelope) (bool, error) {
	var event contract.Event
	if strictJSON(incoming.Payload, &event) != nil || event.ProducerID != session.id {
		return false, errors.New("invalid_host_event")
	}
	// The accept context is cancelled when shutdown begins, but existing Host
	// connections must remain able to commit their final stopped/exited events.
	ack, err := s.db.CommitHostEvent(context.Background(), event)
	if err != nil {
		_ = sendError(session.conn, incoming.RequestID, s.epoch, err)
		return false, nil
	}
	if event.Kind == contract.EventSession || event.Kind == contract.EventQuestion || event.Kind == contract.EventResult || event.Kind == contract.EventFailed || event.Kind == contract.EventUnknown || event.Kind == contract.EventStopped {
		s.notifyActionableEvent()
	}
	body, _ := json.Marshal(ack)
	return true, session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: incoming.RequestID, Epoch: s.epoch, Payload: body})
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
		command, err := s.db.ClaimReady(context.Background(), session.id, s.epoch, 0)
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

func (s *Server) retireCompletedHosts() {
	ids, err := s.db.RetirableHosts(context.Background())
	if err != nil {
		return
	}
	for _, id := range ids {
		if s.db.RetireHost(context.Background(), id) != nil {
			continue
		}
		s.mu.Lock()
		host := s.hosts[id]
		delete(s.hosts, id)
		s.mu.Unlock()
		if host != nil {
			_ = host.conn.Close()
		}
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

func (s *Server) collectionResponse(ctx context.Context, task, status string, page store.PendingPage) (json.RawMessage, error) {
	id, proof, err := s.db.CollectionReceipt(ctx, task, page.Events)
	if err != nil {
		return nil, err
	}
	return json.Marshal(CollectResponse{Version: 1, Status: status, Events: page.Events, NextCursor: page.NextCursor, DeliveryID: id, CollectionProofSHA256: proof})
}
