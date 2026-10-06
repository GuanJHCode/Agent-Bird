package adapter

import (
	"regexp"
	"strconv"
)

var claudeVersion = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)( \(Claude Code\))?$`)

func ClaudeHistorySupported(pin BinaryPin) bool {
	m := claudeVersion.FindStringSubmatch(pin.Version)
	if m == nil {
		return false
	}
	major, e1 := strconv.Atoi(m[1])
	minor, e2 := strconv.Atoi(m[2])
	patch, e3 := strconv.Atoi(m[3])
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	return major > 2 || (major == 2 && (minor > 1 || (minor == 1 && patch >= 234)))
}
