package host

import (
	"errors"
	"reflect"
	"runtime"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// A filtered invocation is read once at entry. Later callbacks cannot switch
// its workspace, action, executable pin or profile after grant admission.
type filterInvocation struct {
	stdin                   []byte
	argv                    []string
	env                     map[string]string
	payload                 adapter.InvocationPayload
	pinPath, pinSHA, output string
	completion              adapter.TaskCompletionMode
}

func (i filterInvocation) Args() []string           { return append([]string(nil), i.argv...) }
func (i filterInvocation) WorkingDirectory() string { return i.payload.Directory }
func (i filterInvocation) Stdin() []byte            { return append([]byte(nil), i.stdin...) }
func (i filterInvocation) Environment() map[string]string {
	out := make(map[string]string, len(i.env))
	for k, v := range i.env {
		out[k] = v
	}
	return out
}
func (i filterInvocation) CandidateWorkspace() *adapter.CandidateWorkspace {
	return i.payload.CandidateWorkspace
}
func (i filterInvocation) CandidateAction() *adapter.CandidateAction {
	return i.payload.CandidateAction
}
func (i filterInvocation) ExecutionProfile() *adapter.ExecutionProfile    { return i.payload.Profile }
func (i filterInvocation) ExecutablePin() (string, string)                { return i.pinPath, i.pinSHA }
func (i filterInvocation) OutputProvider() string                         { return i.output }
func (i filterInvocation) TaskCompletionMode() adapter.TaskCompletionMode { return i.completion }

func admitFilterLaunch(g contract.LaunchCommand) (adapter.InvocationPayload, error) {
	bad := errors.New("git_filter_pipeline_unsupported")
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil || runtime.GOOS != "darwin" || adapter.ValidateGitFilterPipelinePayload(p) != nil || contract.RequireGitFilter(g) != nil || g.Continuation != nil || g.SessionID != "" || g.SessionKind != "" || g.Answer != "" || g.QuestionID != "" || g.QuestionRevision != 0 || g.WorkRevision != 1 {
		return p, bad
	}
	if p.Kind == "provider" {
		if g.Input != nil || g.DependencyInputs != nil || g.ValidationTool != nil {
			return p, bad
		}
	} else if g.Input == nil || g.Input.Event.Artifact == nil || g.Input.Event.Artifact.HostArtifact == nil || g.DependencyInputs == nil || g.DependencyInputs.Version != 1 || len(g.DependencyInputs.Inputs) != 1 || g.Input.Event.TaskID != p.CandidateAction.SourceTask {
		return p, bad
	}
	if p.Kind != "provider" {
		if p.CandidateAction.Operation == "validate" {
			if contract.RequireValidationTool(g, p.CandidateAction.CommandSHA256) != nil {
				return p, bad
			}
		} else if g.ValidationTool != nil {
			return p, bad
		}
	}
	return p, contract.ValidateDependencyInputs(g)
}

func freezeFilterInvocation(inv contract.InvocationView, p adapter.InvocationPayload) (filterInvocation, error) {
	bad := errors.New("git_filter_invocation_mismatch")
	out := filterInvocation{payload: p}
	if inv == nil || inv.WorkingDirectory() != p.Directory {
		return out, bad
	}
	out.argv = append([]string(nil), inv.Args()...)
	if !adapter.IsFilterTargetPreflight(p) && (len(out.argv) == 0 || out.argv[0] == "") {
		return out, bad
	}
	var workspace *adapter.CandidateWorkspace
	if v, ok := inv.(interface {
		CandidateWorkspace() *adapter.CandidateWorkspace
	}); ok {
		workspace = v.CandidateWorkspace()
	}
	var action *adapter.CandidateAction
	if v, ok := inv.(interface {
		CandidateAction() *adapter.CandidateAction
	}); ok {
		action = v.CandidateAction()
	}
	var profile *adapter.ExecutionProfile
	if v, ok := inv.(interface {
		ExecutionProfile() *adapter.ExecutionProfile
	}); ok {
		profile = v.ExecutionProfile()
	}
	if !reflect.DeepEqual(workspace, p.CandidateWorkspace) || !reflect.DeepEqual(action, p.CandidateAction) || !reflect.DeepEqual(profile, p.Profile) {
		return out, bad
	}
	if v, ok := inv.(interface{ ExecutablePin() (string, string) }); ok {
		out.pinPath, out.pinSHA = v.ExecutablePin()
	}
	if v, ok := inv.(interface{ OutputProvider() string }); ok {
		out.output = v.OutputProvider()
	}
	if v, ok := inv.(interface {
		TaskCompletionMode() adapter.TaskCompletionMode
	}); ok {
		out.completion = v.TaskCompletionMode()
	}
	if p.Kind == "provider" || p.Kind == "" {
		if out.argv[0] != p.ProviderLock.Binary.Path || out.pinPath != p.ProviderLock.Binary.Path || out.pinSHA != p.ProviderLock.Binary.SHA256 || out.output != "" && out.output != p.Provider {
			return out, bad
		}
		if p.Kind == "" && (out.output != p.Provider || out.output == "" || out.completion != adapter.TaskCompletionNone) {
			return out, bad
		}
	} else if out.output != "" || out.completion != adapter.TaskCompletionNone {
		return out, bad
	}
	out.stdin = append([]byte(nil), inv.Stdin()...)
	out.env = make(map[string]string)
	for k, v := range inv.Environment() {
		out.env[k] = v
	}
	if adapter.IsFilterTargetPreflight(p) && (len(out.argv) != 0 || len(out.stdin) != 0 || len(out.env) != 0 || out.pinPath != "" || out.pinSHA != "") {
		return out, bad
	}
	return out, nil
}
