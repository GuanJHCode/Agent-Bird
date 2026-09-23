package adapter

// GrokVersionSupported identifies builds covered by the streaming-json-v1
// invocation contract. Keep this additive so existing confirmed locks stay
// valid. New builds still require per-task capability checks and an exact
// binary lock; a matching version never establishes trust or authorization.
func GrokVersionSupported(version string) bool {
	switch version {
	case "grok 1.0.34 (3736acbc8658)", "grok 1.0.40 (eb1a2256660d)":
		return true
	default:
		return false
	}
}
