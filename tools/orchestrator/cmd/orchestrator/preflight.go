package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

// Normalize only existing read-only workspaces before submission. Candidate
// directories may not exist until their dependencies run; their Host-owned
// creation and evidence checks remain authoritative. Never rewrite a grant.
func preflightTasks(tasks []coordinator.TaskRequest) error {
	if len(tasks) == 0 {
		return codeError("invalid_submit")
	}
	for i := range tasks {
		payloads := append([]json.RawMessage{tasks[i].AdapterPayload}, tasks[i].Fallbacks...)
		normalized := make([]json.RawMessage, len(payloads))
		for j, raw := range payloads {
			normalized[j] = raw
			if len(raw) == 0 {
				continue
			}
			var discriminator struct {
				Profile json.RawMessage `json:"profile"`
			}
			if json.Unmarshal(raw, &discriminator) != nil {
				return codeError("invalid_adapter_payload")
			}
			if len(discriminator.Profile) == 0 || string(discriminator.Profile) == "null" {
				continue
			}
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.Profile != nil && p.Profile.Permission == adapter.WorkspaceWrite && p.CandidateWorkspace == nil {
				return codeError("managed_workspace_required")
			}
			if p.Profile == nil || p.CandidateWorkspace != nil || p.CandidateAction != nil {
				continue
			}
			if !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory {
				return codeError("profile_workspace_untrusted")
			}
			canonical, err := filepath.EvalSymlinks(p.Directory)
			if err != nil {
				return codeError("profile_workspace_untrusted")
			}
			info, err := os.Stat(canonical)
			if err != nil || !info.IsDir() {
				return codeError("profile_workspace_untrusted")
			}
			if p.Profile.Role != adapter.Reviewer && canonical != p.Directory {
				return codeError("profile_workspace_untrusted")
			}
			p.Directory = canonical
			normalized[j], err = json.Marshal(p)
			if err != nil {
				return err
			}
		}
		tasks[i].AdapterPayload = normalized[0]
		if len(normalized) > 1 {
			tasks[i].Fallbacks = normalized[1:]
		}
	}
	return nil
}

// This is a filesystem preflight, not proof of provider authentication or of a
// successful launch. No coordinator, grant, Host, or model is started.
func preflight(args []string, out io.Writer) error {
	path, err := requestFile(args)
	if err != nil {
		return err
	}
	var req coordinator.SubmitRequest
	if err = readPrivateJSON(path, &req); err != nil {
		return err
	}
	if err = preflightTasks(req.Tasks); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "preflight_passed", "checks": []string{"existing_profile_workspaces"}, "tasks": req.Tasks})
}
