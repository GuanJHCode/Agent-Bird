package host

import (
	"errors"
	"strings"
)

var errCodexSourceAuthEnvironment = errors.New("codex_source_auth_environment_unsupported")

// Managed file-auth keeps the original environment, but cannot admit an
// alternate auth or endpoint selector. Never normalize/clear a source value or
// include its contents in the refusal. Empty and absent entries remain intact.
func codexValidateSourceEnvironment(env []string) error {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && value != "" && (key == "OPENAI_API_KEY" || key == "CODEX_API_KEY" || key == "OPENAI_BASE_URL") {
			return errCodexSourceAuthEnvironment
		}
	}
	return nil
}
