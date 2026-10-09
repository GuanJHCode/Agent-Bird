package contract

import "testing"

func TestValidationStartFailureFactsRemainDistinct(t *testing.T) {
	for _, kind := range []string{"eagain", "eintr", "probe_code", "started", "probe_state", "unknown_stage"} {
		t.Run(kind, func(t *testing.T) {
			f := &FailureFacts{Version: 1, Stage: "validation_start", Code: "validation_start_eagain", WorkerState: "not_started", ProbeState: "not_applicable"}
			switch kind {
			case "eintr":
				f.Code = "validation_start_eintr"
			case "probe_code":
				f.Code = "probe_start_eagain"
			case "started":
				f.WorkerState = "running"
			case "probe_state":
				f.ProbeState = "not_started"
			case "unknown_stage":
				f.Stage = "arbitrary"
			}
			if f.ValidValidationStart() != (kind == "eagain" || kind == "eintr") || f.ValidPrelaunchProbe() {
				t.Fatal("mixed stage authority", f)
			}
		})
	}
}
