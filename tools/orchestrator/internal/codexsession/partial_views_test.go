package codexsession

import (
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// Pure actual Run/M1/net.Pipe. A display-only view must neither replace a
// completed snapshot nor silently hide an unknown competing final identity.
func TestRunPartialViewsKeepCompletedSnapshotAuthority(t *testing.T) {
	truncated := fixtureFinal()
	truncated["text"] = "{"
	replacement := fixtureFinal()
	replacement["text"] = `{"version":1,"kind":"result","text":"WRONG_SUMMARY_RESULT"}`
	unknownFinal := fixtureFinal()
	unknownFinal["id"] = "unknown-final"
	unknownCommentary := map[string]any{"type": "agentMessage", "id": "unknown-commentary", "phase": "commentary", "text": "display-only"}
	commentary := map[string]any{"type": "agentMessage", "id": "commentary", "phase": "commentary", "text": "original commentary"}
	changedCommentary := map[string]any{"type": "agentMessage", "id": "commentary", "phase": "final_answer", "text": `{"version":1,"kind":"result","text":"WRONG_PROMOTED_COMMENTARY"}`}
	pending := map[string]any{"type": "contextCompaction", "id": "pending"}
	typeDrift := map[string]any{"type": "plan", "id": "pending", "text": "display-only"}
	unfinished := fixtureFinal()
	unfinished["id"] = "unfinished"
	unknownPlan := map[string]any{"type": "plan", "id": "unknown-plan", "text": "display-only"}
	for _, tc := range []struct {
		name, view         string
		completeFinal      bool
		started, completed []map[string]any
		items              []any
		want               error
	}{
		{name: "truncated_same_id_summary", view: "summary", completeFinal: true, items: []any{truncated}},
		{name: "changed_same_id_summary", view: "summary", completeFinal: true, items: []any{replacement}},
		{name: "known_commentary_not_promoted", view: "summary", completeFinal: true, completed: []map[string]any{commentary}, items: []any{changedCommentary}},
		{name: "partial_only_final", view: "summary", items: []any{fixtureFinal()}, want: ErrOutput},
		{name: "unknown_competing_final", view: "summary", completeFinal: true, items: []any{unknownFinal}, want: ErrOutput},
		{name: "completed_type_drift", view: "summary", completeFinal: true, items: []any{map[string]any{"type": "contextCompaction", "id": "final"}}, want: ErrProtocol},
		{name: "started_type_drift", view: "summary", completeFinal: true, started: []map[string]any{pending}, items: []any{typeDrift}, want: ErrProtocol},
		{name: "nonempty_not_loaded", view: "notLoaded", completeFinal: true, items: []any{fixtureFinal()}, want: ErrProtocol},
		{name: "full_conflict_still_rejected", view: "full", completeFinal: true, items: []any{replacement}, want: ErrProtocol},
		{name: "empty_summary", view: "summary", completeFinal: true, items: []any{}},
		{name: "empty_not_loaded", view: "notLoaded", completeFinal: true, items: []any{}},
		{name: "unknown_nonfinal_display_only", view: "summary", completeFinal: true, items: []any{unknownPlan}},
		{name: "unknown_commentary_display_only", view: "summary", completeFinal: true, items: []any{unknownCommentary}},
		{name: "existing_unfinished_final", view: "summary", completeFinal: true, started: []map[string]any{unfinished}, items: []any{}, want: ErrOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
				for _, item := range tc.started {
					if err := p.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 2, "item": item}); err != nil {
						return err
					}
				}
				completed := tc.completed
				if tc.completeFinal {
					completed = append(completed, fixtureFinal())
				}
				for _, item := range completed {
					if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}); err != nil {
						return err
					}
				}
				turn := fixtureTerminal()
				turn["itemsView"] = tc.view
				turn["items"] = tc.items
				return p.terminal(turn)
			}})
			if tc.want != nil {
				requireFailure(t, result, tc.want)
				return
			}
			want := adapter.TaskCompletion{Version: 1, Kind: "technical_question", Text: "Which API?"}
			if result.err != nil || result.out.Completion != want || result.out.Status != "completed" || result.out.TurnID != "turn-1" {
				t.Fatalf("display summary replaced or lost original completion: err=%v outcome=%#v", result.err, result.out)
			}
		})
	}
}

func TestRunPartialDisplayItemDoesNotBecomeCompletedEvent(t *testing.T) {
	plan := map[string]any{"type": "plan", "id": "display-only", "text": "display-only"}
	result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
		if phase != "turn/start" {
			return nil
		}
		if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
			return err
		}
		turn := fixtureTerminal()
		turn["itemsView"] = "summary"
		turn["items"] = []any{plan}
		if err := p.terminal(turn); err != nil {
			return err
		}
		// The actual turn/start reply remains pending, so Run must consume this
		// original post-terminal event before it can return any completion.
		return p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": plan})
	}})
	requireFailure(t, result, ErrProtocol)
}
