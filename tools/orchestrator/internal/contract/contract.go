package contract

import "context"

type Event struct {
	Version     int    `json:"version"`
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	AttemptID   string `json:"attempt_id"`
	SegmentID   string `json:"segment_id"`
	Sequence    int64  `json:"sequence"`
	Kind        string `json:"kind"`
	PayloadHash string `json:"payload_hash"`
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
