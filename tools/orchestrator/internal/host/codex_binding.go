package host

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

var errCodexStateBinding = errors.New("codex_state_binding_invalid")
var codexCanonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// This is a private semantic barrier, not a filesystem proof. The original
// owned root/parent identities and pre-Start seal are verified separately; the
// complete native-created parent chain/file is sealed only after true exit.
// A deferred rollout need not exist when the initial OnThread callback fires.
func codexManagedRolloutRelative(home string, binding codexsession.ThreadBinding) (string, error) {
	if !codexCanonicalPath(home) || home == string(filepath.Separator) || !codexCanonicalUUID.MatchString(binding.ThreadID) || binding.HistoryMode != "paginated" || binding.ModelProvider != "openai" || !codexBoundedIdentity(binding.Model) || !codexCanonicalPath(binding.RolloutPath) {
		return "", errCodexStateBinding
	}
	name := filepath.Base(binding.RolloutPath)
	const prefix = "rollout-"
	if len(name) != len(prefix)+19+1+36+len(".jsonl") || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "-"+binding.ThreadID+".jsonl") {
		return "", errCodexStateBinding
	}
	stamp := name[len(prefix) : len(prefix)+19]
	timestamp, err := time.Parse("2006-01-02T15-04-05", stamp)
	if err != nil || timestamp.Format("2006-01-02T15-04-05") != stamp {
		return "", errCodexStateBinding
	}
	relative := filepath.Join(timestamp.Format("2006/01/02"), name)
	if binding.RolloutPath != filepath.Join(home, "sessions", relative) {
		return "", errCodexStateBinding
	}
	return relative, nil
}

func codexCanonicalPath(path string) bool {
	return len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func codexBoundedIdentity(id string) bool {
	return id != "" && len(id) <= 256 && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}
