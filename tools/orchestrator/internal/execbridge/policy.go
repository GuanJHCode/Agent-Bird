// Package execbridge binds a Codex stdio executor to one isolated worker.
package execbridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

var errPolicy = errors.New("codex_executor_protocol_rejected")

const maxFrame = 1024 * 1024

func validateRequest(raw []byte, toolsEnabled bool) error {
	if len(raw) > maxFrame {
		return errPolicy
	}
	var message map[string]any
	if json.Unmarshal(raw, &message) != nil || !boundedValue(message, 0) {
		return errPolicy
	}
	method, ok := message["method"].(string)
	if !ok || strings.HasPrefix(method, "process/") {
		return errPolicy
	}
	params, ok := message["params"].(map[string]any)
	if !ok {
		return errPolicy
	}
	switch method {
	case "initialize":
		if params["resumeSessionId"] != nil {
			return errPolicy
		}
	case "initialized", "environment/info", "environment/status":
	case "fs/readBlock", "fs/close":
	case "fs/readFile", "fs/open", "fs/getMetadata", "fs/canonicalize", "fs/readDirectory", "fs/walk":
		// Startup can read environment metadata before a turn's external policy is
		// selected. Those reads are still constrained by the executor's OS sandbox.
		if toolsEnabled {
			return validateExternal(params["sandbox"])
		}
	case "fs/writeFile", "fs/createDirectory", "fs/remove", "fs/copy":
		if !toolsEnabled {
			return errPolicy
		}
		return validateExternal(params["sandbox"])
	default:
		return errPolicy
	}
	return nil
}
func validateExternal(raw any) error {
	sandbox, ok := raw.(map[string]any)
	if !ok || !reflect.DeepEqual(sandbox["permissions"], map[string]any{"type": "external", "network": "restricted"}) {
		return errPolicy
	}
	return nil
}
func boundedValue(v any, depth int) bool {
	if depth > 32 {
		return false
	}
	switch v := v.(type) {
	case map[string]any:
		for _, child := range v {
			if !boundedValue(child, depth+1) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !boundedValue(child, depth+1) {
				return false
			}
		}
	}
	return true
}
