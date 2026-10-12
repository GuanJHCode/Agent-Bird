//go:build !windows

package grokorigin

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

var managedCapabilityPaths = []string{
	"/etc/grok/managed_config.toml", "/etc/grok/requirements.toml",
	"/private/etc/grok/managed_config.toml", "/private/etc/grok/requirements.toml",
}

func inspectSystemCapabilities(paths []string) error {
	for _, path := range paths {
		if !validPath(path) {
			return errInvalidRequest
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("grokorigin_system_configuration")
		}
	}
	return nil
}

func originSandboxPolicy(layout *privateLayout, denied []string, authMode string) (string, error) {
	if layout == nil || !layout.same() {
		return "", errPrivateDirectory
	}
	rules := []string{"(version 1)", "(allow default)", "(deny file-write*)", "(deny process-fork)", "(deny file-read* (subpath " + strconv.Quote(layout.state) + "))", `(allow file-write* (literal "/dev/null"))`}
	for _, path := range []string{layout.home, layout.grok, layout.tmp} {
		if !validPath(path) {
			return "", errPrivateDirectory
		}
		rules = append(rules, "(allow file-read* file-write* (subpath "+strconv.Quote(path)+"))")
	}
	switch authMode {
	case "none":
		rules = append(rules, "(deny network*)")
	case "xai_api_key_env":
	default:
		return "", errInvalidRequest
	}
	// Both macOS's public alias and canonical /private/etc path are sealed.
	denied = append(append([]string(nil), denied...), "/etc/grok", "/private/etc/grok")
	size := 0
	for _, path := range denied {
		if !validPath(path) {
			return "", errInvalidRequest
		}
		rule := "(deny file-read* (subpath " + strconv.Quote(path) + "))"
		size += len(rule) + 1
		if size > 64<<10 {
			return "", errors.New("grokorigin_policy_limit")
		}
		rules = append(rules, rule)
	}
	policy := strings.Join(rules, "\n")
	if len(policy) > 64<<10 {
		return "", errors.New("grokorigin_policy_limit")
	}
	return policy, nil
}
