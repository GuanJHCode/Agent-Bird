package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const progressTextLimit = 8192

type providerProgress struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	Available     bool   `json:"available"`
	ReceivedBytes int64  `json:"received_bytes"`
	CompleteLines int64  `json:"complete_lines"`
	AssistantText string `json:"assistant_text,omitempty"`
	TextTruncated bool   `json:"text_truncated"`
	StopReason    string `json:"stop_reason,omitempty"`
}

func boundedProgressText(text string) (string, bool) {
	if len(text) <= progressTextLimit {
		return text, false
	}
	b := []byte(text[:progressTextLimit])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b), true
}

func (p *protocolCollector) progressSnapshot() providerProgress {
	snapshot := providerProgress{Version: 1, Kind: "provider_progress", Status: "incomplete"}
	if !p.mu.TryLock() {
		return snapshot
	}
	defer p.mu.Unlock()
	snapshot.Available = true
	snapshot.ReceivedBytes, snapshot.CompleteLines = p.receivedBytes, p.completeLines
	snapshot.AssistantText, snapshot.TextTruncated = p.partialText, p.partialTruncated
	return snapshot
}

// Record only the known mechanism, never raw errors/paths or inferred Provider
// failures. A negative exit alone does not prove a timeout or owner stop.
func interruptionReason(ctx context.Context, requested bool, exit int) string {
	if requested {
		return "owner_stop_requested"
	}
	switch context.Cause(ctx) {
	case context.DeadlineExceeded:
		return "deadline_exceeded"
	case context.Canceled:
		return "caller_cancelled"
	}
	if cause := context.Cause(ctx); cause != nil {
		switch cause.Error() {
		case "active_budget_exhausted", "profile_timeout", "task_deadline_exceeded":
			return cause.Error()
		}
	}
	if exit < 0 {
		return "process_signaled"
	}
	return "interrupted"
}

// Bounded, best-effort observations; never change model lifetime or business
// outcome. A unique artifact per snapshot keeps all registered hashes stable.
func (h *Host) recordProgress(a store.Attempt, runID, taskID string, meta launchMetadata, p *protocolCollector, identity *process.Identity, index int, started time.Time, reason string) error {
	snapshot := p.progressSnapshot()
	snapshot.ElapsedMS = time.Since(started).Milliseconds()
	snapshot.StopReason = reason
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	sp, err := events.Open(filepath.Join(h.spoolRoot, a.ID, a.SegmentID, "progress", fmt.Sprintf("%03d", index)))
	if err != nil {
		return err
	}
	path, err := sp.WriteArtifact(body)
	if err != nil {
		return err
	}
	ref := &contract.ArtifactRef{ID: "provider-progress", Path: path, Size: int64(len(body)), SHA256: hashBytes(body)}
	if h.progressSink == nil {
		return nil
	}
	event := contract.Event{Version: 1, ProducerID: h.producerID, EventID: fmt.Sprintf("%s:progress:%s:%d", h.producerID, a.SegmentID, index), RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: meta.workRevision, ExecutionEpoch: meta.executionEpoch, CommandID: meta.commandID, Sequence: int64(index + 1), Kind: contract.EventProgress, PayloadHash: ref.SHA256, Artifact: ref}
	return h.progressSink(context.Background(), event)
}

func deadlineCause(reason string) error { return errors.New(reason) }
