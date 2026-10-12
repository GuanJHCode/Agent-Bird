//go:build darwin

package gitfilter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestFilterOriginalStartBoundary(t *testing.T) {
	f := newFixture(t)
	t.Run("preflight_has_no_start", func(t *testing.T) {
		r := f.request(t, "copy", []byte("input"))
		r.ScopeSHA256 = "invalid"
		got, err := Execute(context.Background(), r)
		if err == nil || got.Started || !got.StartedAt.IsZero() || !got.TerminalAt.IsZero() {
			t.Fatalf("preflight invented start: %+v %v", got, err)
		}
	})
	for _, missingStream := range []bool{false, true} {
		name := "accepted_original_boundary"
		if missingStream {
			name = "post_start_error_preserves_boundary"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			r := f.request(t, "copy", []byte("input"))
			var original time.Time
			got, err := execute(ctx, r, func(ctx context.Context, cmd process.Command) (*process.Handle, io.ReadWriteCloser, error) {
				h, stream, err := process.StartDuplex(ctx, cmd)
				if h != nil {
					if state, ok := h.DuplexLaunchState(); ok {
						original = state.SpawnedAt
					}
				}
				if missingStream && h != nil {
					if stream != nil {
						_ = stream.Close()
					}
					return h, nil, errors.Join(err, errors.New("fixture_post_start_error"))
				}
				return h, stream, err
			})
			if got.Unknown || got.Started && (!got.WaitProved || !got.TreeProved) {
				f.retain = true
			}
			t.Logf("original_wait=%t tree=%t exit=%d unknown=%t started=%t error=%v", got.WaitProved, got.TreeProved, got.ExitCode, got.Unknown, got.Started, err)
			if !got.Started || !got.WaitProved || !got.TreeProved || got.Unknown || original.IsZero() {
				t.Fatalf("original launch/terminal proof missing: %+v %v", got, err)
			}
			if missingStream && (err == nil || got.Accepted) || !missingStream && (err != nil || !got.Accepted) {
				t.Fatalf("unexpected outcome: %+v %v", got, err)
			}
			// Exact equality includes the monotonic component; Equal alone or
			// reconstruction from Identity.StartedAt would silently lose it.
			if got.StartedAt != original || original == original.Round(0) {
				t.Fatalf("lost original monotonic boundary: got=%v want=%v", got.StartedAt, original)
			}
			if got.TerminalAt.IsZero() || got.TerminalAt.Before(original) || got.TerminalAt.After(time.Now()) || got.TerminalAt == got.TerminalAt.Round(0) {
				t.Fatalf("missing live original terminal boundary: %v", got.TerminalAt)
			}
			raw, err := json.Marshal(got)
			var wire map[string]json.RawMessage
			var replay Result
			if err != nil || json.Unmarshal(raw, &wire) != nil || json.Unmarshal(raw, &replay) != nil {
				t.Fatalf("invalid result JSON: %s %v", raw, err)
			}
			// Identity already carries a diagnostic wall-clock StartedAt string.
			// Only the new top-level live value must be absent and unrecoverable.
			_, terminalExists := wire["TerminalAt"]
			if _, exists := wire["StartedAt"]; exists || terminalExists || !replay.StartedAt.IsZero() || !replay.TerminalAt.IsZero() {
				t.Fatalf("live boundary became wire data: %s", raw)
			}
		})
	}
}
