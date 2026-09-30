package host

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"path/filepath"
	"testing"
)

// Protocol §6 settles confirmed exits from the actual measured active time so
// provably unused grant can be refunded. A zero omitted field must only ever
// mean "not measured"; the Host owns the only trustworthy clock for a segment
// that ran inside it.
func TestExitedEventReportsMeasuredActiveTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		script  string
		grantMS int64
	}{
		{"normal_exit", "sleep 0.2; echo ok", 5000},
		{"deadline_stop", `trap 'sleep 0.2; exit 0' TERM
while :; do sleep 0.05; done`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "measure-"+tc.name)
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{CommandID: "measure-command-" + tc.name, ReservationID: "measure-reservation-" + tc.name, RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: tc.grantMS}
			inv := launchInvocation{args: []string{"/bin/sh", "-c", tc.script}}
			_, _ = h.ExecuteLaunch(context.Background(), grant, inv)
			sink := &durableSink{}
			if err = h.PublishAll(context.Background(), sink, 1); err != nil {
				t.Fatal(err)
			}
			var exited, stopped *contract.Event
			for i := range sink.events {
				switch sink.events[i].Kind {
				case contract.EventExited:
					exited = &sink.events[i]
				case contract.EventStopped:
					stopped = &sink.events[i]
				}
			}
			if exited == nil {
				t.Fatalf("missing exited event: %#v", sink.events)
			}
			if exited.ActiveMS < 100 {
				t.Fatalf("exited event must carry the measured active time: active=%d", exited.ActiveMS)
			}
			if stopped != nil && stopped.ActiveMS != 0 {
				t.Fatalf("only the exit settlement reports usage: stopped=%#v", *stopped)
			}
		})
	}
}
