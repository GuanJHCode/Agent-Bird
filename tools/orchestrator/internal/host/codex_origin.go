package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

type codexOriginScope struct {
	RunID                string `json:"run_id"`
	TaskID               string `json:"task_id"`
	AttemptID            string `json:"attempt_id"`
	InitialSegmentID     string `json:"initial_segment_id"`
	WorkRevision         int    `json:"work_revision"`
	PlanRevision         int    `json:"plan_revision"`
	OwnerContextID       string `json:"owner_context_id"`
	AdapterPayloadSHA256 string `json:"adapter_payload_sha256"`
}

type codexSourceRecord struct {
	Home            string                 `json:"home"`
	Config          codexStateFile         `json:"config"`
	Auth            codexStateFileIdentity `json:"auth"`
	ProjectedSHA256 string                 `json:"projected_sha256"`
	OverridesSHA256 string                 `json:"overrides_sha256"`
}

type codexOriginRecord struct {
	Version       int                      `json:"version"`
	Scope         codexOriginScope         `json:"scope"`
	Backend       string                   `json:"backend"`
	Lock          adapter.ProviderLock     `json:"provider_lock"`
	Profile       adapter.ExecutionProfile `json:"profile"`
	Workspace     string                   `json:"workspace"`
	ProtectedPath string                   `json:"protected_path"`
	StatePath     string                   `json:"state_path"`
	// One deduplicated map covers source/workspace/protected and original roots.
	Parents              []codexStateDirectory `json:"parents"`
	Source               codexSourceRecord     `json:"source"`
	Owned                codexOwnedReferences  `json:"owned"`
	InstallationID       string                `json:"installation_id"`
	InitialInstallation  codexStateFile        `json:"initial_installation"`
	InitialPrimarySHA256 string                `json:"initial_primary_sha256"`
	InitialAuxiliary     codexAuxiliaryState   `json:"initial_auxiliary"`
	RequiredAbsences     []string              `json:"required_absences"`
}

type codexPreparationControl struct{ BeforePublish func() }

type codexPreparedState struct {
	Origin     codexOriginRecord
	OriginFile codexStateFile
	Protected  codexStateRoot
	Layout     codexStateLayout
	Source     codexSourceReferences
	Projection *codexconfig.Projection
	Descriptor adapter.ManagedCodexDescriptor
	Launch     codexLaunchExpectation  `json:"-"`
	Previous   *codexContinuationState `json:"-"`
}

// Go-only trusted builder output. It never comes from a grant, environment or
// protected source config. The provider lock remains a separate fixed pin.
type codexLaunchExpectation struct{ Path, SHA256 string }

func codexSafeScopeID(id string) bool {
	if !codexBoundedIdentity(id) || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("/\\", r) {
			return false
		}
	}
	return true
}
func codexHexDigest(text string) bool {
	if len(text) != 64 {
		return false
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func codexScopeForGrant(g contract.LaunchCommand, owner string) (codexOriginScope, error) {
	scope := codexOriginScope{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, InitialSegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision, OwnerContextID: owner, AdapterPayloadSHA256: codexDigest(g.AdapterPayload)}
	for _, id := range []string{scope.RunID, scope.TaskID, scope.AttemptID, scope.InitialSegmentID, scope.OwnerContextID, g.CommandID, g.HostID} {
		if !codexSafeScopeID(id) {
			return codexOriginScope{}, errCodexStateFile
		}
	}
	if scope.WorkRevision < 1 || scope.PlanRevision < 1 || g.ExecutionEpoch < 1 {
		return codexOriginScope{}, errCodexStateFile
	}
	return scope, nil
}

func codexSourceHome(env []string) (string, error) {
	values := map[string]string{}
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if ok && (key == "HOME" || key == "CODEX_HOME") {
			if _, exists := values[key]; exists {
				return "", errCodexStateFile
			}
			values[key] = value
		}
	}
	home := values["CODEX_HOME"]
	if home == "" {
		if !codexCanonicalPath(values["HOME"]) {
			return "", errCodexStateFile
		}
		home = filepath.Join(values["HOME"], ".codex")
	}
	if !codexCanonicalPath(home) || home == string(filepath.Separator) {
		return "", errCodexStateFile
	}
	return home, nil
}

func codexPathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func codexCreateDirectory(ctx context.Context, parent codexStateRoot, name string) (codexStateRoot, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return codexStateRoot{}, errCodexStateFile
	}
	chain, err := codexOpenAnchoredStateRoot(ctx, parent)
	if err != nil {
		return codexStateRoot{}, err
	}
	defer func() { chain.close() }()
	parentFD := chain[len(chain)-1].fd
	if unix.Mkdirat(parentFD, name, 0700) != nil || codexAppendStateDirectory(ctx, &chain, name, true) != nil || chain[len(chain)-1].identity.Mode != 0700 || chain.verify(ctx) != nil || unix.Fsync(parentFD) != nil || chain.verify(ctx) != nil {
		return codexStateRoot{}, errCodexStateFile
	}
	return codexStateRoot{Path: filepath.Join(parent.Path, name), Parents: chain.parents()}, nil
}

func codexWriteOwnedLiteral(ctx context.Context, root codexStateRoot, name string, body []byte) error {
	if name != "config.toml" && name != "installation_id" || len(body) < 1 || len(body) > codexconfig.MaxInputBytes || name == "installation_id" && (!codexCanonicalUUID.MatchString(string(body)) || len(body) != 36) {
		return errCodexStateFile
	}
	chain, err := codexOpenAnchoredStateRoot(ctx, root)
	if err != nil {
		return err
	}
	defer chain.close()
	parent := chain[len(chain)-1].fd
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var initial unix.Stat_t
	if unix.Fstat(fd, &initial) != nil || !codexSafeOwnedFile(initial) || uint32(initial.Mode)&07777 != 0600 || initial.Size != 0 {
		return errCodexStateFile
	}
	for left := body; len(left) > 0; {
		if ctx.Err() != nil {
			return errCodexStateFile
		}
		n, err := f.Write(left)
		if err != nil || n == 0 {
			return errCodexStateFile
		}
		left = left[n:]
	}
	if ctx.Err() != nil || f.Sync() != nil {
		return errCodexStateFile
	}
	var done, current unix.Stat_t
	if unix.Fstat(fd, &done) != nil || !codexSafeOwnedFile(done) || uint32(done.Mode)&07777 != 0600 || done.Size != int64(len(body)) || done.Dev != initial.Dev || done.Ino != initial.Ino || unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || codexFileFromStat(done) != codexFileFromStat(current) || chain.verify(ctx) != nil || unix.Fsync(parent) != nil || chain.verify(ctx) != nil {
		return errCodexStateFile
	}
	return nil
}

func codexCreateAuthReference(ctx context.Context, home codexStateRoot, source codexSourceReferences) error {
	chain, err := codexOpenAnchoredStateRoot(ctx, home)
	if err != nil {
		return err
	}
	defer chain.close()
	parent := chain[len(chain)-1].fd
	if unix.Symlinkat(filepath.Join(source.Home, "auth.json"), parent, "auth.json") != nil || chain.verify(ctx) != nil || unix.Fsync(parent) != nil || chain.verify(ctx) != nil {
		return errCodexStateFile
	}
	_, _, err = codexObserveAuthLink(ctx, home, source)
	return err
}

func codexInstallationUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errCodexStateFile
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	hexed := hex.EncodeToString(raw[:])
	return hexed[:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:], nil
}

func codexMergeProtectedParents(groups ...[]codexStateDirectory) ([]codexStateDirectory, error) {
	all := map[string]codexStateDirectory{}
	for _, group := range groups {
		for _, parent := range group {
			if old, exists := all[parent.Path]; exists && old != parent {
				return nil, errCodexStateFile
			}
			all[parent.Path] = parent
			if len(all) > 32 {
				return nil, errCodexStateFile
			}
		}
	}
	result := make([]codexStateDirectory, 0, len(all))
	for _, parent := range all {
		result = append(result, parent)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// First-use owned-state preparation only. No process, Session, turn or seal is
// produced by preparation. Source environment belongs to Host, not a payload.
func (h *Host) prepareManagedCodexState(ctx context.Context, g contract.LaunchCommand, inv adapter.Invocation, env []string, control codexPreparationControl) (codexPreparedState, error) {
	if err := codexValidateSourceEnvironment(env); err != nil {
		return codexPreparedState{}, err
	}
	if g.Continuation != nil {
		return h.restoreManagedCodexState(ctx, g, inv, env, control)
	}
	var empty codexPreparedState
	descriptor, ok := adapter.ManagedCodexDescriptorFor(inv)
	if !ok || descriptor.Backend != adapter.ManagedCodexBackend || descriptor.ResumeThreadID != "" || g.Continuation != nil || g.SessionID != "" || g.SessionKind != "" || ctx.Err() != nil {
		return empty, errCodexStateFile
	}
	h.mu.Lock()
	owner := h.continuationOwner
	h.mu.Unlock()
	scope, err := codexScopeForGrant(g, owner)
	if err != nil {
		return empty, err
	}
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	profile := inv.ExecutionProfile()
	if err != nil || p.Kind != "provider" || p.Provider != string(adapter.ProviderCodex) || p.Profile == nil || profile == nil || *p.Profile != *profile || p.ProviderLock == nil || *p.ProviderLock != descriptor.Lock || p.Directory != inv.WorkingDirectory() || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || p.Prompt+"\n\n"+adapter.ManagedTaskInstruction() != descriptor.Prompt || p.SessionID != "" || p.SessionKind != "" || len(p.Args) != 0 || len(p.Allow) != 0 || len(p.Deny) != 0 || len(p.ExtraArgs) != 0 || p.CandidateAction != nil || p.CandidateWorkspace != nil || p.PermissionMode != "" && p.PermissionMode != "plan" {
		return empty, errCodexStateFile
	}
	sourceHome, err := codexSourceHome(env)
	if err != nil {
		return empty, err
	}
	statePath := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state")
	protectedPath := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID)
	if codexPathsOverlap(sourceHome, statePath) || codexPathsOverlap(sourceHome, protectedPath) || codexPathsOverlap(sourceHome, inv.WorkingDirectory()) || codexPathsOverlap(statePath, inv.WorkingDirectory()) || codexPathsOverlap(protectedPath, inv.WorkingDirectory()) {
		return empty, errCodexStateFile
	}
	source, projection, err := codexCaptureSourceReferences(ctx, sourceHome, codexSourceControl{})
	if err != nil {
		return empty, err
	}
	workspace, err := codexOpenSourceRoot(ctx, inv.WorkingDirectory())
	if err != nil {
		return empty, err
	}
	defer workspace.close()
	protected, err := codexAnchorStateRoot(ctx, protectedPath)
	if err != nil {
		return empty, err
	}
	var previous codexOriginRecord
	if _, err := codexReadProtectedRecord(ctx, protected, "codex-origin.json", &previous); !os.IsNotExist(err) {
		return empty, errCodexStateFile
	}
	attempt, err := codexAnchorStateRoot(ctx, filepath.Dir(statePath))
	if err != nil {
		return empty, err
	}
	state, err := codexCreateDirectory(ctx, attempt, "codex-state")
	if err != nil {
		return empty, err
	}
	for _, name := range []string{"home", "sqlite", "log", "tmp"} {
		if _, err := codexCreateDirectory(ctx, state, name); err != nil {
			return empty, err
		}
	}
	home, err := codexAnchorStateRoot(ctx, filepath.Join(state.Path, "home"))
	if err != nil {
		return empty, err
	}
	for _, name := range []string{"sessions", "skills", "tmp", "thread-writer-locks"} {
		if _, err := codexCreateDirectory(ctx, home, name); err != nil {
			return empty, err
		}
	}
	installation, err := codexInstallationUUID()
	if err != nil {
		return empty, err
	}
	if codexWriteOwnedLiteral(ctx, home, "config.toml", projection.ConfigTOML()) != nil || codexWriteOwnedLiteral(ctx, home, "installation_id", []byte(installation)) != nil || codexCreateAuthReference(ctx, home, source) != nil {
		return empty, errCodexStateFile
	}
	layout, err := codexAnchorStateLayout(ctx, state.Path)
	if err != nil {
		return empty, err
	}
	projection, err = projection.BindRuntimePaths(layout.SQLite.Path, layout.Auxiliary.Runtime[3].Path)
	if err != nil {
		return empty, errCodexStateFile
	}
	owned, err := codexCaptureOwnedReferences(ctx, home, source)
	if err != nil {
		return empty, err
	}
	primary, err := codexCapturePrimaryInventory(ctx, layout, nil, codexPrimaryControl{})
	if err != nil {
		return empty, err
	}
	remaining := codexStateTotalLimit
	var auxObservation codexAuxiliaryObservation
	aux, err := codexCaptureAuxiliaryObserved(ctx, layout.Auxiliary, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}}, &auxObservation)
	if err != nil || aux.Entries != 0 || aux.Bytes != 0 {
		return empty, errCodexStateFile
	}
	small := int64(37)
	initialInstallation, err := codexReadStateFile(ctx, home, "installation_id", codexStateReadControl{FileLimit: 37, Remaining: &small})
	if err != nil || !initialInstallation.File.Present || initialInstallation.File.SHA256 != codexDigest([]byte(installation)) {
		return empty, errCodexStateFile
	}
	parents, err := codexMergeProtectedParents(primary.Parents, source.Parents, workspace.parents(), protected.Parents)
	if err != nil {
		return empty, err
	}
	origin := codexOriginRecord{Version: 1, Scope: scope, Backend: descriptor.Backend, Lock: descriptor.Lock, Profile: *profile, Workspace: inv.WorkingDirectory(), ProtectedPath: protected.Path, StatePath: state.Path, Parents: parents, Source: codexSourceRecord{Home: source.Home, Config: source.Config, Auth: source.Auth, ProjectedSHA256: source.ProjectedSHA256, OverridesSHA256: source.OverridesSHA256}, Owned: owned, InstallationID: installation, InitialInstallation: initialInstallation.File, InitialPrimarySHA256: primary.SHA256, InitialAuxiliary: aux, RequiredAbsences: codexRequiredAbsences()}
	if codexValidateOriginRecord(origin) != nil {
		return empty, errCodexStateFile
	}
	body, err := json.Marshal(origin)
	if err != nil || len(body) > 16<<10 {
		return empty, errCodexStateFile
	}
	if control.BeforePublish != nil {
		control.BeforePublish()
	}
	current, err := codexCapturePrimaryInventory(ctx, layout, nil, codexPrimaryControl{})
	if err != nil || !reflect.DeepEqual(primary, current) || codexVerifySourceReferences(ctx, source) != nil || codexVerifyOwnedReferences(ctx, home, source, owned) != nil || codexVerifyAuxiliaryObservation(ctx, auxObservation) != nil || workspace.verify(ctx) != nil {
		return empty, errCodexStateFile
	}
	originFile, err := codexWriteProtectedRecord(ctx, protected, "codex-origin.json", body)
	if err != nil {
		return empty, err
	}
	prepared := codexPreparedState{Origin: origin, OriginFile: originFile, Protected: protected, Layout: layout, Source: source, Projection: projection, Descriptor: descriptor}
	if codexVerifyInitialPrepared(ctx, prepared) != nil {
		return empty, errCodexStateFile
	}
	return prepared, nil
}
