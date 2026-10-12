// Package gitfilter executes a caller-owned sealed filter over bounded streams.
// It does not create storage, authenticate grants, retry, or delete caller files.
package gitfilter

import (
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const MaximumBytes int64 = 16 << 20
const MaximumToolBytes int64 = 32 << 20

type Tool struct {
	Path, SHA256 string
	Size         int64
}
type Request struct {
	Operation, ScopeSHA256 string
	Tool                   Tool
	Args                   []string
	Input, Output          *os.File
	EmptyDirectory         string
}
type Result struct {
	// TerminalAt exists only after this Handle proved Wait and tree exit.
	TerminalAt time.Time `json:"-"`
	// StartedAt is a live monotonic observation, never a durable process proof.
	StartedAt                                             time.Time `json:"-"`
	Accepted, Started, WaitProved, TreeProved, Unknown    bool
	ScopeSHA256, RequestSHA256, InputSHA256, OutputSHA256 string
	InputBytes, OutputBytes                               int64
	Identity                                              process.Identity
	ExitCode                                              int
}
