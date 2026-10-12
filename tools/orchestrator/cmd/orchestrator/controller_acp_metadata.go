package main

type controllerACPMetadata struct {
	Version                int    `json:"version"`
	TransportMode          string `json:"transport_mode"`
	RunID                  string `json:"run_id"`
	OwnerMode              string `json:"owner_mode"`
	OwnerCapabilityFile    string `json:"owner_capability_file"`
	ControlFile            string `json:"control_file"`
	OwnerContextID         string `json:"owner_context_id"`
	OwnerPID               int    `json:"owner_pid"`
	OwnerBirth             string `json:"owner_birth"`
	BindingRequestID       string `json:"binding_request_id"`
	BindingID              string `json:"binding_id"`
	OwnerEpoch             uint64 `json:"owner_epoch"`
	NativePID              int    `json:"native_pid"`
	NativeBirth            string `json:"native_birth"`
	NativeExecutableSHA256 string `json:"native_executable_sha256"`
	NativeSessionID        string `json:"native_session_id"`
	NativeExitKnown        bool   `json:"native_exit_known"`
	NativeExitCode         int    `json:"native_exit_code"`
	NativeTreeExited       bool   `json:"native_tree_exited"`
	ManifestSHA256         string `json:"manifest_sha256"`
	Status                 string `json:"status"`
}

func replaceControllerACPMetadata(path string, expected, next controllerACPMetadata) error {
	if err := privateHandleParent(path); err != nil {
		return err
	}
	raw, err := readBridgeRequestFile(path)
	if err != nil {
		return err
	}
	var prior controllerACPMetadata
	if err = decode(raw, &prior); err != nil || prior != expected {
		return codeError("controller_acp_metadata_conflict")
	}
	return replacePrivateJSON(path, next)
}

func controllerACPMode(provider, plan, handle string, sessionWrite bool, request string, args []string) (bool, error) {
	invalid := codeError("invalid_args")
	if (plan == "") != (handle == "") {
		return false, invalid
	}
	if provider != "grok" {
		if sessionWrite || request != "" || (plan != "" && provider != "claude" && provider != "agy") {
			return false, invalid
		}
		return false, nil
	}
	if plan == "" {
		if sessionWrite || request != "" {
			return false, invalid
		}
		return false, nil
	}
	if !sessionWrite || request == "" || len(args) != 0 {
		return false, invalid
	}
	return true, nil
}
