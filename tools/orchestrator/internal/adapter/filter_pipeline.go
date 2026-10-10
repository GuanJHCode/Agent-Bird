package adapter

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// This is admission, not decoding or retry permission. The initial filter
// shapes include a separate target observation, never target write authority.
func ValidateGitFilterPipelinePayload(p InvocationPayload) error {
	bad := errors.New("git_filter_pipeline_unsupported")
	if p.GitFilter == nil || p.GitFilter.Validate() != nil || !filterAbsolutePath(p.Directory) || p.SessionID != "" || p.SessionKind != "" || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || len(p.Args) != 0 || len(p.ExtraArgs) != 0 || len(p.Allow) != 0 || len(p.Deny) != 0 {
		return bad
	}
	if p.Kind == "provider" {
		w, l, profile := p.CandidateWorkspace, p.ProviderLock, p.Profile
		if p.CandidateAction != nil || w == nil || w.Version != 1 || !w.AutoDirectory || w.Rework != nil || w.NewBaseRework != nil || !filterAbsolutePath(w.RepoRoot) || !filterCommitOID(w.BaseOID) || len(w.Paths) < 1 || len(w.Paths) > 256 || l == nil || l.Version != 1 || (l.Provider != ProviderClaude && l.Provider != ProviderAGY) || p.Provider != string(l.Provider) || l.Protocol != ProtocolID(l.Provider) || !filterAbsolutePath(l.Binary.Path) || l.Binary.Version == "" || !ValidCommandSHA256(l.Binary.SHA256) || profile == nil || profile.Version != 1 || profile.Role != Implementer || profile.Permission != WorkspaceWrite || profile.GrokSessionWrite || profile.TimeoutMS < 1 || profile.TimeoutMS > 3600000 {
			return bad
		}
		for i, f := range w.Paths {
			if f == "" || len(f) > 512 || path.IsAbs(f) || path.Clean(f) != f || strings.ContainsAny(f, "\\\x00\r\n") || i > 0 && w.Paths[i-1] >= f {
				return bad
			}
			for _, part := range strings.Split(f, "/") {
				if part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".gitattributes") {
					return bad
				}
			}
			for _, prior := range w.Paths[:i] {
				a, b := strings.ToLower(f), strings.ToLower(prior)
				if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
					return bad
				}
			}
		}
		return nil
	}
	a := p.CandidateAction
	if p.Kind == "candidate" && a != nil && a.Operation == "filter_target_preflight" {
		if p.Provider != "" || p.Profile != nil || p.ProviderLock != nil || p.CandidateWorkspace != nil || p.Prompt != "" || p.PermissionMode != "" || a.Version != 1 || a.DependencyInputVersion != 1 || !a.AutoDirectory || a.SourceTask == "" || len(a.OrderedSourceTasks) != 0 || a.ReviewIndex != nil || len(a.Command) != 0 || a.CommandSHA256 != "" || !filterAbsolutePath(a.Target.Worktree) || !filterCommitOID(a.Target.BaseOID) || !filterTargetBranch(a.Target.Ref) {
			return bad
		}
		return nil
	}
	if p.Kind == "" && a != nil && (a.Operation == "review" || a.Operation == "filter_target_review") {
		l, profile := p.ProviderLock, p.Profile
		if a.Operation == "review" && a.Target != (CandidateTarget{}) || a.Operation == "filter_target_review" && (!filterAbsolutePath(a.Target.Worktree) || !filterCommitOID(a.Target.BaseOID) || !filterTargetBranch(a.Target.Ref)) {
			return bad
		}
		if p.CandidateWorkspace != nil || p.PermissionMode != "" || a.Version != 1 || a.DependencyInputVersion != 1 || !a.AutoDirectory || a.SourceTask == "" || len(a.OrderedSourceTasks) != 0 || a.ReviewIndex != nil || len(a.Command) != 0 || a.CommandSHA256 != "" || l == nil || l.Version != 1 || (l.Provider != ProviderClaude && l.Provider != ProviderAGY) || p.Provider != string(l.Provider) || l.Protocol != ProtocolID(l.Provider) || !filterAbsolutePath(l.Binary.Path) || l.Binary.Version == "" || !ValidCommandSHA256(l.Binary.SHA256) || profile == nil || profile.Version != 1 || profile.Role != Reviewer || profile.Permission != ReadOnly || profile.GrokSessionWrite || profile.TimeoutMS < 1 || profile.TimeoutMS > 3600000 {
			return bad
		}
		return nil
	}
	if p.Kind != "candidate" || p.Provider != "" || p.Profile != nil || p.ProviderLock != nil || p.CandidateWorkspace != nil || p.Prompt != "" || p.PermissionMode != "" || a == nil || a.Version != 1 || a.Operation != "validate" || a.DependencyInputVersion != 1 || !a.AutoDirectory || a.SourceTask == "" || len(a.OrderedSourceTasks) != 0 || a.ReviewIndex != nil || a.Target != (CandidateTarget{}) || len(a.Command) < 1 || len(a.Command) > 64 || !filterAbsolutePath(a.Command[0]) || !ValidCommandSHA256(a.CommandSHA256) {
		return bad
	}
	bytes := 0
	for _, arg := range a.Command {
		bytes += len(arg)
		if strings.ContainsRune(arg, 0) || bytes > 256<<10 {
			return bad
		}
	}
	return nil
}

func filterAbsolutePath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\r\n")
}
func filterCommitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IsFilterTargetPreflight recognizes the complete process-free action shape.
func IsFilterTargetPreflight(p InvocationPayload) bool {
	return p.CandidateAction != nil && p.CandidateAction.Operation == "filter_target_preflight" && ValidateGitFilterPipelinePayload(p) == nil
}
func filterTargetBranch(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 512 || path.Clean(ref) != ref || strings.ContainsAny(ref, " ~^:?*[\\") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return false
		}
	}
	return true
}

// IsFilterTargetReview recognizes the fresh readonly target approval shape.
func IsFilterTargetReview(p InvocationPayload) bool {
	return p.CandidateAction != nil && p.CandidateAction.Operation == "filter_target_review" && ValidateGitFilterPipelinePayload(p) == nil
}
