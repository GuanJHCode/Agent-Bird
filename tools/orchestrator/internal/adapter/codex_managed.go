package adapter

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const ManagedCodexBackend = "codex-app-server-m3-v1"

// Copied immutable metadata for the dedicated Host path. Runtime state paths,
// source config/auth, process ownership and physical seals are owned by Host.
type ManagedCodexDescriptor struct {
	Backend        string
	Prompt         string
	ResumeThreadID string
	Lock           ProviderLock
}

var managedCodexThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// This internal constructor never calls the guarded general/legacy constructor.
// There is no request mode/env/extra-argument switch to select this backend.
func BuildManagedCodexInvocation(req Request) (Invocation, error) {
	invalid := errors.New("codex_managed_invocation_invalid")
	if req.Provider != ProviderCodex || req.Profile == nil || req.Lock == nil || req.Action != nil || req.Workspace != nil || len(req.ExtraArgs) != 0 || len(req.Permission.Allow) != 0 || len(req.Permission.Deny) != 0 || (req.Permission.Mode != "" && req.Permission.Mode != "plan") {
		return Invocation{}, invalid
	}
	p := *req.Profile
	if p.CodexHostToolsVersion != 0 || p.Version != 1 || p.Role != Reviewer || p.Permission != ReadOnly || !p.CLIModelDefault || p.Model != "" || p.Reasoning != "" || p.GrokSessionWrite || p.GrokExistingAuthOnly || p.TimeoutMS < 1 || p.TimeoutMS > 3600000 {
		return Invocation{}, invalid
	}
	if !managedCodexPath(req.Binary.Path) || req.Binary.Version != CodexVersion || req.Binary.SHA256 != CodexSHA256 || req.Lock.Version != 1 || req.Lock.Provider != ProviderCodex || req.Lock.Protocol != ProtocolID(ProviderCodex) || req.Lock.Binary != req.Binary || !managedCodexPath(req.CWD) || !utf8.ValidString(req.Prompt) || len(req.Prompt) > 65536 || strings.TrimSpace(req.Prompt) == "" || strings.ContainsRune(req.Prompt, 0) {
		return Invocation{}, invalid
	}
	if req.Continuation == nil {
		if req.Session.Kind != "" || req.Session.ID != "" {
			return Invocation{}, invalid
		}
	} else if ValidateContinuationReference(req.Continuation) != nil || req.Continuation.Mode != contract.ContinuationQuestion || req.Continuation.SourceKind != "host" || req.Continuation.WorkspaceSourceSegmentID != "" || req.Session.Kind != SessionID || !managedCodexThreadID.MatchString(req.Session.ID) {
		return Invocation{}, invalid
	}
	descriptor := ManagedCodexDescriptor{Backend: ManagedCodexBackend, Prompt: req.Prompt + "\n\n" + managedTaskInstruction, ResumeThreadID: req.Session.ID, Lock: *req.Lock}
	return Invocation{provider: ProviderCodex, pin: req.Binary, argv: []string{req.Binary.Path, "app-server", "--strict-config"}, cwd: req.CWD, env: map[string]string{}, profile: &p, completionMode: TaskCompletionV1, managedCodex: &descriptor}, nil
}

func managedCodexPath(path string) bool {
	return len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

// Taking the actual concrete Invocation is deliberate: a custom InvocationView
// that imitates a descriptor method cannot manufacture its private selector.
func ManagedCodexDescriptorFor(inv Invocation) (ManagedCodexDescriptor, bool) {
	if inv.managedCodex == nil {
		return ManagedCodexDescriptor{}, false
	}
	return *inv.managedCodex, true
}
