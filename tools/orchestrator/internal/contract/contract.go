package contract

import (
	"context"
	"encoding/json"
)

type Event struct {
	Version          int              `json:"version"`
	ProducerID       string           `json:"producer_id,omitempty"`
	EventID          string           `json:"event_id,omitempty"`
	EventRevision    int64            `json:"event_revision,omitempty"`
	ActionSlot       string           `json:"action_slot,omitempty"`
	RunID            string           `json:"run_id"`
	TaskID           string           `json:"task_id"`
	AttemptID        string           `json:"attempt_id"`
	SegmentID        string           `json:"segment_id"`
	WorkRevision     int              `json:"work_revision,omitempty"`
	ExecutionEpoch   uint64           `json:"execution_epoch,omitempty"`
	CommandID        string           `json:"command_id,omitempty"`
	Sequence         int64            `json:"sequence"`
	Kind             string           `json:"kind"`
	PayloadHash      string           `json:"payload_hash"`
	Process          *ProcessIdentity `json:"process,omitempty"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	Artifact         *ArtifactRef     `json:"artifact,omitempty"`
	ActiveMS         int64            `json:"active_ms,omitempty"`
	QuestionID       string           `json:"question_id,omitempty"`
	QuestionRevision int              `json:"question_revision,omitempty"`
	QuestionKind     string           `json:"question_kind,omitempty"`
	SessionKind      string           `json:"session_kind,omitempty"`
	SessionID        string           `json:"session_id,omitempty"`
	StopReason       string           `json:"stop_reason,omitempty"`
}

const (
	EventPrepared = "prepared"
	EventSpawned  = "spawned"
	EventRunning  = "running"
	EventProgress = "progress"
	EventSession  = "session"
	EventQuestion = "question"
	EventResult   = "result"
	EventExited   = "exited"
	EventStopped  = "stopped"
	EventFailed   = "failed"
	EventUnknown  = "unknown"
)

type ProcessIdentity struct {
	PID   int    `json:"pid"`
	Birth string `json:"birth"`
	PGID  int    `json:"pgid"`
}

type ArtifactRef struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Question struct {
	ID       string
	TaskID   string
	Revision int
	Status   string
}
type Decision string

const (
	DecisionHandled     Decision = "handled"
	DecisionWaitingUser Decision = "waiting_user"
	DecisionStale       Decision = "stale"
	DecisionRejected    Decision = "rejected"
)

type Segment struct {
	AttemptID, ID string
	Number        int
	Revision      int
}
type HostIdentity struct {
	OriginContextID, HostGeneration string
	PID                             int
	Birth                           string
	Executable                      string
}

// HostHello binds a source Host process to a coordinator-approved launch.
// Credentials and environment data are deliberately absent.
type HostHello struct {
	LaunchID        string `json:"launch_id"`
	LaunchToken     string `json:"launch_token"`
	OriginContextID string `json:"origin_context_id"`
	OriginPID       int    `json:"origin_pid"`
	OriginBirth     string `json:"origin_birth"`
	HostGeneration  string `json:"host_generation"`
	PID             int    `json:"pid"`
	Birth           string `json:"birth"`
	Executable      string `json:"executable"`
}

type HostReady struct {
	HostID           string `json:"host_id"`
	CoordinatorEpoch uint64 `json:"coordinator_epoch"`
}

type HostReconciled struct {
	ProducerID       string `json:"producer_id"`
	PublishedThrough int64  `json:"published_through"`
}

// LaunchCommand contains ownership and budget grants only. The source Host
// retains the invocation and authentication environment locally.
type AcceptedInput struct {
	Event             Event           `json:"event"`
	AdapterPayload    json.RawMessage `json:"adapter_payload"`
	DecisionCommandID string          `json:"decision_command_id"`
}

type LaunchCommand struct {
	Input              *AcceptedInput  `json:"input,omitempty"`
	PlanRevision       int             `json:"plan_revision,omitempty"`
	CommandID          string          `json:"command_id"`
	HostID             string          `json:"host_id"`
	RunID              string          `json:"run_id"`
	TaskID             string          `json:"task_id"`
	AttemptID          string          `json:"attempt_id"`
	SegmentID          string          `json:"segment_id"`
	WorkRevision       int             `json:"work_revision"`
	ExecutionEpoch     uint64          `json:"execution_epoch"`
	SlotToken          string          `json:"slot_token"`
	LaunchIntentHash   string          `json:"launch_intent_hash"`
	ReservationID      string          `json:"reservation_id"`
	BudgetGroupID      string          `json:"budget_group_id"`
	GrantedActiveMS    int64           `json:"granted_active_ms"`
	DeadlineUnixMS     int64           `json:"deadline_unix_ms"`
	AdapterPayload     json.RawMessage `json:"adapter_payload,omitempty"`
	QuestionID         string          `json:"question_id,omitempty"`
	QuestionRevision   int             `json:"question_revision,omitempty"`
	Answer             string          `json:"answer,omitempty"`
	SessionKind        string          `json:"session_kind,omitempty"`
	SessionID          string          `json:"session_id,omitempty"`
	ReportCapabilityID string          `json:"report_capability_id"`
}

type ReportCapabilityRegistration struct {
	CapabilityID   string `json:"capability_id"`
	TokenHash      string `json:"token_hash"`
	CapabilityDir  string `json:"capability_dir"`
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	SegmentID      string `json:"segment_id"`
	ProducerID     string `json:"producer_id"`
	WorkRevision   int    `json:"work_revision"`
	ExecutionEpoch uint64 `json:"execution_epoch"`
}

type StopCommand struct {
	CommandID      string `json:"command_id"`
	SegmentID      string `json:"segment_id"`
	Reason         string `json:"reason"`
	DeadlineUnixMS int64  `json:"deadline_unix_ms"`
}

type DurableAck struct {
	ProducerID   string `json:"producer_id"`
	AckedThrough int64  `json:"acked_through"`
	EventID      string `json:"event_id"`
	PayloadHash  string `json:"payload_hash"`
	Status       string `json:"status"`
}
type Command struct {
	Path string
	Args []string
	Dir  string
}
type Result struct {
	Status       string
	ExitCode     int
	EventCount   int
	OutputHash   string
	ArtifactPath string
}
type Adapter interface {
	Probe(context.Context) error
	Start(context.Context, Segment) error
	ReadEvents(context.Context, Segment) ([]Event, error)
	ResumeWithAnswer(context.Context, Segment, string) error
	Cancel(context.Context, Segment) error
	CollectResult(context.Context, Segment) (Result, error)
}

// InvocationView is the narrow adapter-to-Host handoff. The Host owns process creation.
type InvocationView interface {
	Args() []string
	WorkingDirectory() string
	Stdin() []byte
	Environment() map[string]string
}
