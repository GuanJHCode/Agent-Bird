package host

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"golang.org/x/sys/unix"
)

func codexRequiredAbsences() []string { return []string{"models_cache.json", "session_index.jsonl"} }

func codexValidFileIdentity(identity codexStateFileIdentity) bool {
	return identity.Inode > 0 && identity.UID == uint32(unix.Geteuid()) && identity.Nlink == 1 && identity.Mode <= 07777 && identity.Mode&07022 == 0 && identity.Size >= 0 && identity.Size <= codexStateFileLimit && identity.MtimeNsec >= 0 && identity.MtimeNsec < 1000000000 && identity.CtimeNsec >= 0 && identity.CtimeNsec < 1000000000
}
func codexValidStateFile(file codexStateFile, required bool) bool {
	if !file.Present {
		return !required && file.Identity == (codexStateFileIdentity{}) && file.SHA256 == ""
	}
	return codexValidFileIdentity(file.Identity) && codexHexDigest(file.SHA256)
}
func codexOriginParentMap(origin codexOriginRecord) (map[string]codexStateDirectory, error) {
	if len(origin.Parents) < 1 || len(origin.Parents) > 32 {
		return nil, errCodexStateFile
	}
	result := map[string]codexStateDirectory{}
	for i, parent := range origin.Parents {
		if !codexCanonicalPath(parent.Path) || parent.Inode == 0 || parent.Mode > 07777 || i > 0 && origin.Parents[i-1].Path >= parent.Path {
			return nil, errCodexStateFile
		}
		result[parent.Path] = parent
	}
	return result, nil
}
func codexRecordedParents(path string, all map[string]codexStateDirectory) ([]codexStateDirectory, error) {
	if !codexCanonicalPath(path) || path == string(filepath.Separator) {
		return nil, errCodexStateFile
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts)+1 > 32 {
		return nil, errCodexStateFile
	}
	current := string(filepath.Separator)
	paths := []string{current}
	for _, part := range parts {
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	result := make([]codexStateDirectory, 0, len(paths))
	for _, path := range paths {
		parent, ok := all[path]
		if !ok {
			return nil, errCodexStateFile
		}
		result = append(result, parent)
	}
	return result, nil
}

func codexValidateOriginRecord(o codexOriginRecord) error {
	for _, id := range []string{o.Scope.RunID, o.Scope.TaskID, o.Scope.AttemptID, o.Scope.InitialSegmentID, o.Scope.OwnerContextID} {
		if !codexSafeScopeID(id) {
			return errCodexStateFile
		}
	}
	if o.Version != 1 || o.Scope.WorkRevision < 1 || o.Scope.PlanRevision < 1 || !codexHexDigest(o.Scope.AdapterPayloadSHA256) || (o.Backend != adapter.ManagedCodexBackend && o.Backend != adapter.CodexPluginBackend) || o.Lock.Version != 1 || o.Lock.Provider != adapter.ProviderCodex || o.Lock.Protocol != adapter.ProtocolID(adapter.ProviderCodex) || o.Lock.Binary.Version != adapter.CodexVersion || o.Lock.Binary.SHA256 != adapter.CodexSHA256 || !codexCanonicalPath(o.Lock.Binary.Path) || o.Lock.Binary.Path == string(filepath.Separator) {
		return errCodexStateFile
	}
	p := o.Profile
	if (o.Backend == adapter.CodexPluginBackend && p.CodexHostToolsVersion != 1) || (o.Backend == adapter.ManagedCodexBackend && p.CodexHostToolsVersion != 0) {
		return errCodexStateFile
	}
	if p.Version != 1 || p.Role != adapter.Reviewer || p.Permission != adapter.ReadOnly || !p.CLIModelDefault || p.Model != "" || p.Reasoning != "" || p.GrokSessionWrite || p.GrokExistingAuthOnly || p.TimeoutMS < 1 || p.TimeoutMS > 3600000 {
		return errCodexStateFile
	}
	for _, path := range []string{o.Workspace, o.StatePath, o.ProtectedPath, o.Source.Home} {
		if !codexCanonicalPath(path) || path == string(filepath.Separator) {
			return errCodexStateFile
		}
	}
	if filepath.Base(o.ProtectedPath) != o.Scope.InitialSegmentID || filepath.Base(filepath.Dir(o.ProtectedPath)) != o.Scope.AttemptID || o.StatePath != filepath.Join(filepath.Dir(o.ProtectedPath), "codex-state") || codexPathsOverlap(o.Source.Home, o.StatePath) || codexPathsOverlap(o.Source.Home, o.ProtectedPath) || codexPathsOverlap(o.Workspace, o.StatePath) || codexPathsOverlap(o.Workspace, o.Source.Home) || codexPathsOverlap(o.Workspace, o.ProtectedPath) {
		return errCodexStateFile
	}
	if !codexValidStateFile(o.Source.Config, false) || !codexValidFileIdentity(o.Source.Auth) || !codexHexDigest(o.Source.ProjectedSHA256) || !codexHexDigest(o.Source.OverridesSHA256) || !codexValidStateFile(o.Owned.Config, true) || o.Owned.Config.Identity.Mode != 0600 || o.Owned.Config.SHA256 != o.Source.ProjectedSHA256 || o.Owned.AuthTarget != filepath.Join(o.Source.Home, "auth.json") {
		return errCodexStateFile
	}
	link := o.Owned.AuthLink
	// The single declared symlink is a reference exception, never a safe regular.
	// Darwin applies umask to link mode (e.g.0755); retain its actual non-special
	// permission bits. Link write authority is its anchored parent/sandbox, not
	// symlink permission bits; subsequent verification requires exact identity.
	if link.Inode == 0 || link.UID != uint32(unix.Geteuid()) || link.Nlink != 1 || link.Mode > 0777 || link.Size != int64(len(o.Owned.AuthTarget)) || link.MtimeNsec < 0 || link.MtimeNsec >= 1000000000 || link.CtimeNsec < 0 || link.CtimeNsec >= 1000000000 {
		return errCodexStateFile
	}
	if !codexCanonicalUUID.MatchString(o.InstallationID) || !codexValidStateFile(o.InitialInstallation, true) || o.InitialInstallation.Identity.Mode != 0600 || o.InitialInstallation.Identity.Size != 36 || o.InitialInstallation.SHA256 != codexDigest([]byte(o.InstallationID)) || !codexHexDigest(o.InitialPrimarySHA256) || o.InitialAuxiliary.Version != 1 || o.InitialAuxiliary.Entries != 0 || o.InitialAuxiliary.Bytes != 0 || !codexHexDigest(o.InitialAuxiliary.SHA256) || !reflect.DeepEqual(o.RequiredAbsences, codexRequiredAbsences()) {
		return errCodexStateFile
	}
	_, err := codexOriginParentMap(o)
	return err
}

func codexCheckRequiredAbsences(ctx context.Context, home codexStateRoot) error {
	chain, err := codexOpenAnchoredStateRoot(ctx, home)
	if err != nil {
		return err
	}
	defer chain.close()
	for _, name := range codexRequiredAbsences() {
		var stat unix.Stat_t
		if err := unix.Fstatat(chain[len(chain)-1].fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != unix.ENOENT {
			return errCodexStateFile
		}
	}
	return chain.verify(ctx)
}

// Reconstruct only fixed roots from the exact origin map; no newly observed
// directory becomes trusted merely because Anchor found it safe today.
func codexLayoutFromOrigin(ctx context.Context, o codexOriginRecord) (codexStateLayout, codexSourceReferences, codexStateRoot, error) {
	var empty codexStateLayout
	if codexValidateOriginRecord(o) != nil {
		return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
	}
	all, err := codexOriginParentMap(o)
	if err != nil {
		return empty, codexSourceReferences{}, codexStateRoot{}, err
	}
	layout, err := codexAnchorStateLayout(ctx, o.StatePath)
	if err != nil {
		return empty, codexSourceReferences{}, codexStateRoot{}, err
	}
	roots := []codexStateRoot{layout.State, layout.Home, layout.SQLite, layout.Sessions}
	roots = append(roots, layout.Auxiliary.Runtime[:]...)
	used := map[string]bool{}
	for _, root := range roots {
		expected, err := codexRecordedParents(root.Path, all)
		if err != nil || !reflect.DeepEqual(root.Parents, expected) {
			return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
		}
		for _, parent := range expected {
			used[parent.Path] = true
		}
	}
	var sourceParents []codexStateDirectory
	for _, path := range []string{o.Source.Home, o.Workspace} {
		chain, err := codexOpenSourceRoot(ctx, path)
		if err != nil {
			return empty, codexSourceReferences{}, codexStateRoot{}, err
		}
		expected, recordErr := codexRecordedParents(path, all)
		current := chain.parents()
		verifyErr := chain.verify(ctx)
		chain.close()
		if recordErr != nil || verifyErr != nil || !reflect.DeepEqual(current, expected) {
			return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
		}
		for _, parent := range expected {
			used[parent.Path] = true
		}
		if path == o.Source.Home {
			sourceParents = expected
		}
	}
	protected, err := codexAnchorStateRoot(ctx, o.ProtectedPath)
	if err != nil {
		return empty, codexSourceReferences{}, codexStateRoot{}, err
	}
	expected, err := codexRecordedParents(protected.Path, all)
	if err != nil || !reflect.DeepEqual(protected.Parents, expected) {
		return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
	}
	for _, parent := range expected {
		used[parent.Path] = true
	}
	if len(used) != len(all) {
		return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
	}
	source := codexSourceReferences{Home: o.Source.Home, Parents: sourceParents, Config: o.Source.Config, Auth: o.Source.Auth, ProjectedSHA256: o.Source.ProjectedSHA256, OverridesSHA256: o.Source.OverridesSHA256}
	if codexVerifySourceReferences(ctx, source) != nil || codexVerifyOwnedReferences(ctx, layout.Home, source, o.Owned) != nil || codexCheckRequiredAbsences(ctx, layout.Home) != nil {
		return empty, codexSourceReferences{}, codexStateRoot{}, errCodexStateFile
	}
	return layout, source, protected, nil
}

func codexVerifyInitialPrepared(ctx context.Context, p codexPreparedState) error {
	sqlite, log, bound := p.Projection.RuntimePaths()
	if !bound || sqlite != p.Layout.SQLite.Path || log != p.Layout.Auxiliary.Runtime[3].Path || codexDigest(p.Projection.ConfigTOML()) != p.Source.ProjectedSHA256 {
		return errCodexStateFile
	}
	layout, source, protected, err := codexLayoutFromOrigin(ctx, p.Origin)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(layout, p.Layout) || !reflect.DeepEqual(source, p.Source) || !reflect.DeepEqual(protected, p.Protected) {
		return errCodexStateFile
	}
	var actual codexOriginRecord
	originFile, err := codexReadProtectedRecord(ctx, protected, "codex-origin.json", &actual)
	if err != nil || originFile != p.OriginFile || !reflect.DeepEqual(actual, p.Origin) {
		return errCodexStateFile
	}
	primary, err := codexCapturePrimaryInventory(ctx, layout, nil, codexPrimaryControl{})
	if err != nil || primary.SHA256 != p.Origin.InitialPrimarySHA256 {
		return errCodexStateFile
	}
	remaining := codexStateTotalLimit
	var observed codexAuxiliaryObservation
	aux, err := codexCaptureAuxiliaryObserved(ctx, layout.Auxiliary, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}}, &observed)
	if err != nil || aux != p.Origin.InitialAuxiliary {
		return errCodexStateFile
	}
	finalPrimary, err := codexCapturePrimaryInventory(ctx, layout, nil, codexPrimaryControl{})
	if err != nil || !reflect.DeepEqual(finalPrimary, primary) || codexVerifyAuxiliaryObservation(ctx, observed) != nil {
		return errCodexStateFile
	}
	all, _ := codexOriginParentMap(p.Origin)
	if codexCheckPersistentSlotMetadata(ctx, layout.Home, "installation_id", p.Origin.InitialInstallation, all) != nil || codexVerifySourceReferences(ctx, source) != nil || codexVerifyOwnedReferences(ctx, layout.Home, source, p.Origin.Owned) != nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}
