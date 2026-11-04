package host

import (
	"strings"
	"testing"
)

func TestCodexEmptyIntermediateDoesNotReplaceFinalOrReuseDraft(t *testing.T) {
	start := `{"type":"thread.started","thread_id":"thread"}`
	empty := `{"type":"item.completed","item":{"id":"empty","type":"agent_message","text":""}}`
	answer := `{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"answer"}}`
	completed := `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`
	for _, tc := range []struct {
		name      string
		lines     []string
		wantError string
	}{
		{"empty intermediate then final", []string{start, empty, answer, completed}, ""},
		{"empty final clears draft", []string{start, answer, empty, completed}, "provider_result_missing"},
		{"only empty final", []string{start, empty, completed}, "provider_result_missing"},
		{"empty before session", []string{empty}, "provider_event_before_session"},
		{"empty after terminal", []string{start, answer, completed, empty}, "provider_event_after_terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector, err := newProtocolCollector("codex-cli")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = collector.Write([]byte(strings.Join(tc.lines, "\n") + "\n")); err != nil {
				t.Fatal(err)
			}
			terminal, _, err := collector.Finish()
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("terminal=%#v err=%v want=%s", terminal, err, tc.wantError)
				}
				return
			}
			if err != nil || terminal.Text != "answer" || terminal.Status != "completed" {
				t.Fatalf("terminal=%#v err=%v", terminal, err)
			}
		})
	}
}
