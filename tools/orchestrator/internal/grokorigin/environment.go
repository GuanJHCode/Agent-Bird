//go:build !windows

package grokorigin

import (
	"errors"
	"strings"
)

// The lookup function is deliberately not invoked in unauthenticated mode.
// Callers pass os.LookupEnv only here; no inherited environment is merged.
func originEnvironment(mode, home, grok, tmp string, lookup func(string) (string, bool)) ([]string, error) {
	if !validPath(home) || !validPath(grok) || !validPath(tmp) {
		return nil, errInvalidRequest
	}
	env := []string{"HOME=" + home, "GROK_HOME=" + grok, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "TERM=dumb", "NO_COLOR=1"}
	switch mode {
	case "none":
		return env, nil
	case "xai_api_key_env":
		if lookup == nil {
			return nil, errors.New("grokorigin_auth_invalid")
		}
		key, ok := lookup("XAI_API_KEY")
		if !ok || !requestText(key, 8192) || strings.TrimSpace(key) != key {
			return nil, errors.New("grokorigin_auth_invalid")
		}
		return append(env, "XAI_API_KEY="+key), nil
	default:
		return nil, errors.New("grokorigin_auth_invalid")
	}
}
