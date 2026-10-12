package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

type Event struct {
	CandidateResource         json.RawMessage            `json:"candidate_resource,omitempty"`
	TemporaryWorkspaceCleanup *TemporaryWorkspaceCleanup `json:"temporary_workspace_cleanup,omitempty"`
	ValidationToolCleanup     *ValidationToolCleanup     `json:"validation_tool_cleanup,omitempty"`
	GitFilterCarrierCleanup   *GitFilterCarrierCleanup   `json:"git_filter_carrier_cleanup,omitempty"`
	GitFilterCleanup          *GitFilterCleanup          `json:"git_filter_cleanup,omitempty"`
	DiagnosticsFailure        *DiagnosticsFailure        `json:"diagnostics_failure,omitempty"`
	Failure                   *FailureFacts              `json:"failure,omitempty"`
	Version                   int                        `json:"version"`
	ProducerID                string                     `json:"producer_id,omitempty"`
	EventID                   string                     `json:"event_id,omitempty"`
	EventRevision             int64                      `json:"event_revision,omitempty"`
	ActionSlot                string                     `json:"action_slot,omitempty"`
	RunID                     string                     `json:"run_id"`
	TaskID                    string                     `json:"task_id"`
	AttemptID                 string                     `json:"attempt_id"`
	SegmentID                 string                     `json:"segment_id"`
	WorkRevision              int                        `json:"work_revision,omitempty"`
	ExecutionEpoch            uint64                     `json:"execution_epoch,omitempty"`
	CommandID                 string                     `json:"command_id,omitempty"`
	Sequence                  int64                      `json:"sequence"`
	Kind                      string                     `json:"kind"`
	PayloadHash               string                     `json:"payload_hash"`
	Process                   *ProcessIdentity           `json:"process,omitempty"`
	ExitCode                  *int                       `json:"exit_code,omitempty"`
	Artifact                  *ArtifactRef               `json:"artifact,omitempty"`
	ActiveMS                  int64                      `json:"active_ms,omitempty"`
	QuestionID                string                     `json:"question_id,omitempty"`
	QuestionRevision          int                        `json:"question_revision,omitempty"`
	QuestionKind              string                     `json:"question_kind,omitempty"`
	SessionKind               string                     `json:"session_kind,omitempty"`
	SessionID                 string                     `json:"session_id,omitempty"`
	StopReason                string                     `json:"stop_reason,omitempty"`
}

type DiagnosticsFailure struct {
	Version  int  `json:"version"`
	Summary  bool `json:"summary,omitempty"`
	Detailed bool `json:"detailed,omitempty"`
}

func (d DiagnosticsFailure) Validate() error {
	if d.Version != 1 || (!d.Summary && !d.Detailed) {
		return errors.New("diagnostics_failure_invalid")
	}
	return nil
}
func (d *DiagnosticsFailure) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("diagnostics_failure_invalid")
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return errors.New("diagnostics_failure_invalid")
		}
	}
	type plain DiagnosticsFailure
	var v plain
	if err := DecodeBridgeStrict(raw, &v); err != nil {
		return errors.New("diagnostics_failure_invalid")
	}
	if err := DiagnosticsFailure(v).Validate(); err != nil {
		return err
	}
	*d = DiagnosticsFailure(v)
	return nil
}
func ValidateDiagnosticsFailure(e Event) error {
	if e.DiagnosticsFailure == nil {
		return nil
	}
	if e.Kind != EventExited {
		return errors.New("diagnostics_failure_invalid")
	}
	return e.DiagnosticsFailure.Validate()
}

const (
	EventPrepared       = "prepared"
	EventSpawned        = "spawned"
	EventRunning        = "running"
	EventProgress       = "progress"
	EventSession        = "session"
	EventQuestion       = "question"
	EventResult         = "result"
	EventExited         = "exited"
	EventStopped        = "stopped"
	EventFailed         = "failed"
	EventLaunchRejected = "launch_rejected"
	EventUnknown        = "unknown"
)

type ProcessIdentity struct {
	PID   int    `json:"pid"`
	Birth string `json:"birth"`
	PGID  int    `json:"pgid"`
}

type ArtifactRef struct {
	ID           string               `json:"id"`
	Path         string               `json:"path"`
	Size         int64                `json:"size"`
	SHA256       string               `json:"sha256"`
	HostArtifact *HostArtifactReceipt `json:"host_artifact,omitempty"`
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
	// ResumeCommandID selects an already-authorized succession. It is not
	// authority and never changes the physical identity or recovery scope.
	ResumeCommandID string `json:"resume_command_id,omitempty"`
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

// DependencyInputVector fixes every direct dependency accepted by a v1 join.
// Input remains the content-bearing source selected by CandidateAction.
type DependencyInputVector struct {
	Version int             `json:"version"`
	Inputs  []AcceptedInput `json:"inputs"`
}

type LaunchCommand struct {
	ValidationTool     *ValidationToolGrant    `json:"validation_tool,omitempty"`
	GitFilterCarrier   *GitFilterCarrierGrant  `json:"git_filter_carrier,omitempty"`
	GitFilter          *GitFilterResourceGrant `json:"git_filter,omitempty"`
	HostArtifacts      *HostArtifactGrant      `json:"host_artifacts,omitempty"`
	Diagnostics        *DiagnosticsGrant       `json:"diagnostics,omitempty"`
	Continuation       *ContinuationRef        `json:"continuation,omitempty"`
	Input              *AcceptedInput          `json:"input,omitempty"`
	DependencyInputs   *DependencyInputVector  `json:"dependency_inputs,omitempty"`
	PlanRevision       int                     `json:"plan_revision,omitempty"`
	CommandID          string                  `json:"command_id"`
	HostID             string                  `json:"host_id"`
	RunID              string                  `json:"run_id"`
	TaskID             string                  `json:"task_id"`
	AttemptID          string                  `json:"attempt_id"`
	SegmentID          string                  `json:"segment_id"`
	WorkRevision       int                     `json:"work_revision"`
	ExecutionEpoch     uint64                  `json:"execution_epoch"`
	SlotToken          string                  `json:"slot_token"`
	LaunchIntentHash   string                  `json:"launch_intent_hash"`
	ReservationID      string                  `json:"reservation_id"`
	BudgetGroupID      string                  `json:"budget_group_id"`
	GrantedActiveMS    int64                   `json:"granted_active_ms"`
	DeadlineUnixMS     int64                   `json:"deadline_unix_ms"`
	AdapterPayload     json.RawMessage         `json:"adapter_payload,omitempty"`
	QuestionID         string                  `json:"question_id,omitempty"`
	QuestionRevision   int                     `json:"question_revision,omitempty"`
	Answer             string                  `json:"answer,omitempty"`
	SessionKind        string                  `json:"session_kind,omitempty"`
	SessionID          string                  `json:"session_id,omitempty"`
	ReportCapabilityID string                  `json:"report_capability_id"`
}

// ContinuationRef is derived from durable owner decisions and trusted Host
// initialization. It is never accepted in a submitted adapter payload.
type ContinuationRef struct {
	SourceKind               string `json:"source_kind"`
	Version                  int    `json:"version"`
	Mode                     string `json:"mode"`
	SourceSegmentID          string `json:"source_segment_id"`
	SourceEventID            string `json:"source_event_id"`
	SourceEventRevision      int64  `json:"source_event_revision"`
	SourceEventSHA256        string `json:"source_event_sha256"`
	SessionEventID           string `json:"session_event_id"`
	AdapterPayloadSHA256     string `json:"adapter_payload_sha256"`
	OwnerContextID           string `json:"owner_context_id"`
	WorkspaceSourceSegmentID string `json:"workspace_source_segment_id,omitempty"`
}

const (
	ContinuationQuestion    = "question"
	ContinuationInterrupted = "interrupted"
)

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
