package adapter

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

func ValidCommandSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

// Pure admission/profile guard; never probes an executable or touches Git.
func ValidationRetryPayload(raw []byte) (InvocationPayload, error) {
	p, err := DecodeInvocationPayload(raw)
	a := p.CandidateAction
	if err != nil || p.GitFilter != nil || p.Kind != "candidate" || a == nil || a.Version != 1 || a.Operation != "validate" || !a.AutoDirectory || a.ReviewIndex != nil || a.DependencyInputVersion < 0 || a.DependencyInputVersion > 1 || a.SourceTask == "" || len(a.Command) < 1 || len(a.Command) > 64 || !ValidCommandSHA256(a.CommandSHA256) || !filepath.IsAbs(a.Command[0]) || filepath.Clean(a.Command[0]) != a.Command[0] || p.Provider != "" || p.Profile != nil || p.ProviderLock != nil || p.CandidateWorkspace != nil || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || p.SessionID != "" || p.SessionKind != "" || len(p.Args) != 0 || len(p.ExtraArgs) != 0 || p.PermissionMode != "" || len(p.Allow) != 0 || len(p.Deny) != 0 || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory || p.Directory == string(filepath.Separator) {
		return p, errors.New("candidate_validation_retry_unsupported")
	}
	return p, nil
}
