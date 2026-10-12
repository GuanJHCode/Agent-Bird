package store

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const (
	callResultArtifactLimit = 1 << 20
	callResultPageBytes     = 8192
)

// CallResult is an authenticated, bounded view of one original terminal event.
// Reading it neither accepts the business result nor acknowledges delivery.
type CallResult struct {
	Version       int    `json:"version"`
	Status        string `json:"status"`
	ProcessExited bool   `json:"process_exited"`
	EventKind     string `json:"event_kind"`
	PayloadSHA256 string `json:"payload_sha256"`
	Content       string `json:"content"`
	NextOffset    int    `json:"next_offset"`
	TotalBytes    int    `json:"total_bytes"`
}

func (d *DB) ReadCallResult(ctx context.Context, taskID, controller, token string, offset int) (CallResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out CallResult
	if err := d.ValidateTaskOwner(ctx, taskID, controller, token); err != nil {
		return out, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var segment, status string
	err = tx.QueryRowContext(ctx, `SELECT t.status,s.status,s.id FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segments s ON s.attempt_id=a.id WHERE t.id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, taskID).Scan(&out.Status, &status, &segment)
	if err != nil {
		return out, CodeError("call_result_not_quiescent")
	}
	var body string
	err = tx.QueryRowContext(ctx, `SELECT body_json FROM runtime_events WHERE task_id=? AND segment_id=? AND json_extract(body_json,'$.kind') IN ('result','failed','stopped','question','launch_rejected') ORDER BY sequence DESC LIMIT 1`, taskID, segment).Scan(&body)
	var event contract.Event
	if err != nil || json.Unmarshal([]byte(body), &event) != nil || event.TaskID != taskID || event.SegmentID != segment {
		return CallResult{}, CodeError("call_result_unavailable")
	}
	if (status != "exited" || event.Kind == contract.EventLaunchRejected) && (status != "not_started" || event.Kind != contract.EventLaunchRejected) {
		return CallResult{}, CodeError("call_result_not_quiescent")
	}
	content := []byte(nil)
	if event.Artifact != nil {
		content, err = readCommittedHostArtifact(ctx, tx, event, callResultArtifactLimit)
		if err != nil {
			return CallResult{}, err
		}
		if !utf8.Valid(content) {
			return CallResult{}, CodeError("call_result_not_text")
		}
	}
	if offset < 0 || offset > len(content) || (offset < len(content) && offset > 0 && !utf8.RuneStart(content[offset])) {
		return CallResult{}, CodeError("call_result_offset_invalid")
	}
	end := offset + callResultPageBytes
	if end > len(content) {
		end = len(content)
	}
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	out.Version = 1
	out.ProcessExited = event.Kind != contract.EventLaunchRejected
	out.EventKind = event.Kind
	out.PayloadSHA256 = event.PayloadHash
	out.Content = string(content[offset:end])
	out.NextOffset = end
	out.TotalBytes = len(content)
	if err := tx.Commit(); err != nil {
		return CallResult{}, err
	}
	return out, nil
}
