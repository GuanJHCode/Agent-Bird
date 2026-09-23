package host

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/workspaceread"
)

func codexReadTools(ctx context.Context, reader *workspaceread.Reader) []codexrpc.DynamicTool {
	makeTool := func(name, description string, fields map[string]string, call func(map[string]any) (any, error)) codexrpc.DynamicTool {
		properties := map[string]any{}
		required := []string{}
		for name, kind := range fields {
			properties[name] = map[string]any{"type": kind}
			required = append(required, name)
		}
		sort.Strings(required)
		return codexrpc.DynamicTool{Name: name, Description: description, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, Call: func(args map[string]any) (string, error) {
			if len(args) != len(fields) {
				return "", errors.New("workspace_tool_arguments_invalid")
			}
			for key, kind := range fields {
				switch kind {
				case "string":
					if _, ok := args[key].(string); !ok {
						return "", errors.New("workspace_tool_arguments_invalid")
					}
				case "integer":
					v, ok := args[key].(float64)
					if !ok || v < 0 || v > 1_000_000 || v != float64(int64(v)) {
						return "", errors.New("workspace_tool_arguments_invalid")
					}
				}
			}
			value, err := call(args)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(value)
			if err != nil || len(raw) > 65536 {
				return "", errors.New("workspace_tool_output_limit")
			}
			return string(raw), nil
		}}
	}
	return []codexrpc.DynamicTool{
		makeTool("bird_list_files", "List regular code files in this isolated workspace. Paths are relative; use directory '.' for the root, after '' for the first page, and limit 1..200. Follow next_after when truncated. No shell commands are available.", map[string]string{"directory": "string", "after": "string", "limit": "integer"}, func(a map[string]any) (any, error) {
			return reader.List(ctx, a["directory"].(string), a["after"].(string), int(a["limit"].(float64)))
		}),
		makeTool("bird_read_file", "Read a text file in this isolated workspace. Use a relative path, start_line >=1 and limit 1..200. Continue at next_line when truncated. Use native apply_patch for edits. The main CLI runs commands and tests after collecting the candidate.", map[string]string{"path": "string", "start_line": "integer", "limit": "integer"}, func(a map[string]any) (any, error) {
			return reader.Read(ctx, a["path"].(string), int(a["start_line"].(float64)), int(a["limit"].(float64)))
		}),
		makeTool("bird_search_text", "Search text files with a bounded Go regular expression. Use directory '.' for all code, after '' initially, and limit 1..100. Results may be truncated; refine the pattern or read matching files. This tool cannot execute commands.", map[string]string{"pattern": "string", "directory": "string", "after": "string", "limit": "integer"}, func(a map[string]any) (any, error) {
			return reader.Search(ctx, a["pattern"].(string), a["directory"].(string), a["after"].(string), int(a["limit"].(float64)))
		}),
	}
}

func codexDisabledFeatures() []string {
	return []string{"shell_tool", "unified_exec", "unified_exec_tty", "shell_snapshot", "shell_snapshot_v2", "deferred_executor", "code_mode", "code_mode_only", "hooks", "multi_agent", "multi_agent_v2", "apps", "plugins", "request_permissions_tool", "skill_mcp_dependency_install", "memories", "view_image", "sleep_tool", "in_app_browser", "browser_use", "browser_use_full_cdp_access", "browser_use_external", "computer_use", "image_generation", "tool_suggest", "recommended_plugins", "enable_mcp_apps", "standalone_web_search", "web_search_request", "web_search_cached", "in_app_local_automation", "in_app_updates", "in_app_chat", "in_app_dictation"}
}

// The pinned native runtime defaults an unspecified reviewer to User. Managed
// requirements are rejected during preparation; preserve explicit choices and
// the raw source projection rather than rewriting third-party configuration.
func codexThreadReviewer(config map[string]any) any {
	if config["approvals_reviewer"] == nil {
		return "user"
	}
	return config["approvals_reviewer"]
}

func verifyCodexReadEditConfig(config map[string]any) error {
	invalid := errors.New("codex_read_edit_policy_unverified")
	features, ok := config["features"].(map[string]any)
	if !ok {
		return invalid
	}
	for _, name := range codexDisabledFeatures() {
		if features[name] != false {
			return invalid
		}
	}
	agents, ok := config["agents"].(map[string]any)
	if !ok || agents["enabled"] != false || config["web_search"] != "disabled" {
		return invalid
	}
	tools, _ := config["tools"].(map[string]any)
	if raw := tools["experimental_request_user_input"]; raw != nil {
		userInput, ok := raw.(map[string]any)
		if !ok || userInput["enabled"] != false {
			return invalid
		}
	}
	if raw := config["mcp_servers"]; raw != nil {
		servers, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		for _, value := range servers {
			server, ok := value.(map[string]any)
			if !ok || server["enabled"] != false {
				return invalid
			}
		}
	}
	return nil
}
