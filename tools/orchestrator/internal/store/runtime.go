package store

const (
	defaultSegmentActiveMS = int64(60 * 60 * 1000)
	defaultGroupActiveMS   = int64(120 * 60 * 1000)
)

const (
	ErrNoSlot            CodeError = "no_execution_slot"
	ErrNoReady           CodeError = "no_ready_task"
	ErrInvalidDAG        CodeError = "invalid_dag"
	ErrHostRejected      CodeError = "host_rejected"
	ErrEventSequence     CodeError = "event_sequence_conflict"
	ErrAdmissionDeferred CodeError = "admission_deferred"
)

type HostLaunchSpec struct {
	OriginContextID string
	HostGeneration  string
	Executable      string
}

type PlanSpec struct {
	Run          RunSpec
	Host         HostLaunchSpec
	Tasks        []TaskSpec
	LaunchID     string
	LaunchToken  string
	ControlToken string
}

type SubmitReceipt struct {
	LaunchID     string `json:"launch_id"`
	LaunchToken  string `json:"launch_token"`
	ControlToken string `json:"control_token"`
}

type HostBinding struct {
	HostID string
	PID    int
	Birth  string
	Active int
}
