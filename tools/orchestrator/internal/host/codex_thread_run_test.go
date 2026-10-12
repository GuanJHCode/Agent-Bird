package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Pure net.Pipe + owned FS. The phases are synthetic; only the real M1/M3 jobs,
// callback, protected publication and durable spool ordering are established.
func TestCodexThreadCallbackGatesActualM3Turn(t *testing.T) {
	for _, kind := range []string{"exact", "nullable_path", "legacy_history"} {
		t.Run(kind, func(t *testing.T) {
			h, p, g, id, b := codexThreadFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			local, remote := net.Pipe()
			client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			methods := []string{}
			go func() {
				defer remote.Close()
				dec, enc := json.NewDecoder(remote), json.NewEncoder(remote)
				for _, method := range []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"} {
					var frame map[string]json.RawMessage
					if err := dec.Decode(&frame); err != nil {
						if kind != "exact" && method == "turn/start" && err == io.EOF {
							done <- nil
						} else {
							done <- errors.New("missing managed phase")
						}
						return
					}
					var actual string
					_ = json.Unmarshal(frame["method"], &actual)
					methods = append(methods, actual)
					if actual != method {
						done <- errors.New("turn before durable binding")
						return
					}
					if method == "turn/start" {
						spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
						if err != nil {
							done <- err
							return
						}
						records, err := spool.Read()
						if err != nil || len(records) != 3 || records[2].Kind != contract.EventSession {
							done <- errors.New("turn before durable Host Session")
							return
						}
					}
					if method == "initialized" {
						continue
					}
					var result map[string]any
					switch method {
					case "initialize":
						result = map[string]any{"codexHome": p.Layout.Home.Path, "platformFamily": "unix", "platformOs": "macos", "userAgent": "pure-fixture"}
					case "config/read":
						result = map[string]any{"config": codexThreadEffectiveFixture(p), "origins": map[string]any{}}
					case "thread/start":
						thread := map[string]any{"id": b.ThreadID, "preview": "", "ephemeral": false, "modelProvider": "openai", "createdAt": 1, "updatedAt": 1, "status": map[string]any{"type": "idle"}, "path": b.RolloutPath, "historyMode": b.HistoryMode, "cwd": p.Origin.Workspace, "cliVersion": "0.154.0", "source": "appServer", "sessionId": "synthetic-session", "projectId": nil, "turns": []any{}}
						if kind == "nullable_path" {
							thread["path"] = nil
						}
						if kind == "legacy_history" {
							thread["historyMode"] = "legacy"
						}
						result = map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": p.Origin.Workspace, "model": b.Model, "modelProvider": "openai", "reasoningEffort": "source-effort", "serviceTier": nil, "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": thread}
					case "turn/start":
						result = map[string]any{"turn": map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress", "startedAt": 2}}
					}
					if err := enc.Encode(map[string]any{"id": frame["id"], "result": result}); err != nil {
						done <- err
						return
					}
				}
				item := map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": "{\"version\":1,\"kind\":\"technical_question\",\"text\":\"Which file?\"}"}
				if err := enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": b.ThreadID, "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{item}, "startedAt": 2, "completedAt": 3, "error": nil}}}); err != nil {
					done <- err
					return
				}
				var extra any
				if err := dec.Decode(&extra); err != io.EOF {
					done <- errors.New("extra turn or unjoined transport")
					return
				}
				done <- nil
			}()
			out, receipt, runErr := h.runManagedCodexTurn(ctx, client, p, g, id)
			peerErr := <-done
			if peerErr != nil {
				t.Fatal(peerErr)
			}
			select {
			case <-client.Done():
			default:
				t.Fatal("Run detached transport")
			}
			spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			events, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "exact" {
				if runErr != nil || out.Completion.Kind != "technical_question" || out.Completion.Text != "Which file?" || receipt.Session.EventID == "" || len(events) != 3 || events[2].Kind != contract.EventSession {
					t.Fatal("binding/turn consumer missing", runErr, len(events))
				}
			} else if !errors.Is(runErr, codexsession.ErrCallback) || runErr.Error() != codexsession.ErrCallback.Error() || out.Completion.Kind != "" || receipt.Session.EventID != "" || len(events) != 2 || !reflect.DeepEqual(methods, []string{"initialize", "initialized", "config/read", "thread/start"}) {
				t.Fatal("invalid persistence binding reached Session/turn", runErr, methods)
			}
		})
	}
}

// Minimal fixed public defaults plus the managed policy, with no source auth,
// raw native data or claims about the native provider's behavior.
func codexThreadEffectiveFixture(p codexPreparedState) map[string]any {
	features := map[string]any{}
	for _, key := range []string{"codex_hooks", "hooks", "plugin_hooks", "plugins", "apps", "enable_mcp_apps", "multi_agent", "multi_agent_v2", "collab", "browser_use", "computer_use", "memories", "memory_tool", "shell_snapshot", "shell_snapshot_v2", "remote_control", "background_paginated_rollout_migration", "auth_elicitation", "mcp_2026_07_28", "mentions_v2", "network_proxy", "remote_plugin", "tool_suggest", "windows_sandbox_service", "goals"} {
		features[key] = false
	}
	return map[string]any{
		"model": "gpt-fixed", "model_reasoning_effort": "source-effort", "cli_auth_credentials_store": "file", "sandbox_mode": "read-only", "approval_policy": "on-request", "approvals_reviewer": "user", "check_for_update_on_startup": false,
		"features": features, "notify": []any{}, "history": map[string]any{"persistence": "none"}, "analytics": map[string]any{"enabled": false}, "web_search": "disabled",
		"sqlite_home": p.Layout.SQLite.Path, "log_dir": p.Layout.Auxiliary.Runtime[3].Path, "allow_login_shell": false, "chatgpt_base_url": "https://chatgpt.com/backend-api/",
		"background_terminal_max_timeout": int64(300000), "file_opener": "vscode", "hide_agent_reasoning": false, "include_apps_instructions": true, "include_collaboration_mode_instructions": true, "include_environment_context": true, "include_permissions_instructions": true,
		"mcp_oauth_credentials_store": "auto", "project_doc_max_bytes": int64(32768), "project_doc_fallback_filenames": []any{}, "project_root_markers": []any{".git"},
		"shell_environment_policy": map[string]any{"inherit": "core", "ignore_default_excludes": false, "experimental_use_profile": false, "exclude": nil, "include_only": nil, "set": nil, "filters": nil},
		"orchestrator":             map[string]any{"skills": map[string]any{"enabled": false}, "mcp": map[string]any{"enabled": false}},
	}
}
