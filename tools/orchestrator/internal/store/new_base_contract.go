package store

// NewBaseReworkSpec names one accepted integration and an exact plan derivation.
// Credentials are deliberately separate from durable nonsecret lineage.
type NewBaseReworkSpec struct {
	Version                 int               `json:"version"`
	SourceIntegrationTaskID string            `json:"source_integration_task_id"`
	SourceEvent             ReworkEvent       `json:"source_event"`
	TaskMap                 map[string]string `json:"task_map"`
	Feedback                string            `json:"feedback"`
	Acceptance              string            `json:"acceptance"`
	CommandID               string            `json:"command_id"`
}
type NewBaseAuthorization struct {
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
}
