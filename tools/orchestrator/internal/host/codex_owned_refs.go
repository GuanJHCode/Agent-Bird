package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"golang.org/x/sys/unix"
)

type codexOwnedReferences struct {
	Config     codexStateFile         `json:"config"`
	AuthLink   codexStateFileIdentity `json:"auth_link"`
	AuthTarget string                 `json:"auth_target"`
}

type codexSealableObservation struct {
	Physical codexPhysicalObservation
	Source   codexSourceReferences
	Owned    codexOwnedReferences
}

func codexObserveAuthLink(ctx context.Context, home codexStateRoot, source codexSourceReferences) (codexStateFileIdentity, string, error) {
	chain, err := codexOpenAnchoredStateRoot(ctx, home)
	if err != nil {
		return codexStateFileIdentity{}, "", err
	}
	defer chain.close()
	var before, after unix.Stat_t
	fd := chain[len(chain)-1].fd
	if unix.Fstatat(fd, "auth.json", &before, unix.AT_SYMLINK_NOFOLLOW) != nil || uint32(before.Mode)&unix.S_IFMT != unix.S_IFLNK || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size < 1 || before.Size > 4096 {
		return codexStateFileIdentity{}, "", errCodexStateFile
	}
	target := make([]byte, 4097)
	n, err := unix.Readlinkat(fd, "auth.json", target)
	if err != nil || n < 1 || n > 4096 || string(target[:n]) != filepath.Join(source.Home, "auth.json") || !codexCanonicalPath(string(target[:n])) || unix.Fstatat(fd, "auth.json", &after, unix.AT_SYMLINK_NOFOLLOW) != nil || codexFileFromStat(after) != codexFileFromStat(before) || chain.verify(ctx) != nil {
		return codexStateFileIdentity{}, "", errCodexStateFile
	}
	return codexFileFromStat(before), string(target[:n]), nil
}

func codexCaptureOwnedReferences(ctx context.Context, home codexStateRoot, source codexSourceReferences) (codexOwnedReferences, error) {
	var refs codexOwnedReferences
	remaining := int64(1 << 20)
	config, err := codexReadStateFile(ctx, home, "config.toml", codexStateReadControl{FileLimit: 1 << 20, Remaining: &remaining})
	if err != nil || !config.File.Present || config.File.Identity.Mode != 0600 || config.File.SHA256 != source.ProjectedSHA256 {
		return refs, errCodexStateFile
	}
	link, target, err := codexObserveAuthLink(ctx, home, source)
	if err != nil {
		return refs, err
	}
	refs = codexOwnedReferences{Config: config.File, AuthLink: link, AuthTarget: target}
	if codexVerifyOwnedReferences(ctx, home, source, refs) != nil {
		return codexOwnedReferences{}, errCodexStateFile
	}
	return refs, nil
}

func codexVerifyOwnedReferences(ctx context.Context, home codexStateRoot, source codexSourceReferences, refs codexOwnedReferences) error {
	parents := map[string]codexStateDirectory{}
	for _, parent := range home.Parents {
		parents[parent.Path] = parent
	}
	if !refs.Config.Present || refs.Config.Identity.Mode != 0600 || refs.Config.SHA256 != source.ProjectedSHA256 || codexCheckPersistentSlotMetadata(ctx, home, "config.toml", refs.Config, parents) != nil {
		return errCodexStateFile
	}
	link, target, err := codexObserveAuthLink(ctx, home, source)
	if err != nil || link != refs.AuthLink || target != refs.AuthTarget {
		return errCodexStateFile
	}
	return nil
}

func codexCaptureSealableObservation(ctx context.Context, layout codexStateLayout, binding codexsession.ThreadBinding, installation string, source codexSourceReferences, owned codexOwnedReferences, read codexStateReadControl) (codexSealableObservation, error) {
	var observation codexSealableObservation
	if codexVerifySourceReferences(ctx, source) != nil || codexVerifyOwnedReferences(ctx, layout.Home, source, owned) != nil {
		return observation, errCodexStateFile
	}
	observation.Source, observation.Owned = source, owned
	_, err := codexCapturePhysicalObserved(ctx, layout, binding, installation, read, &observation.Physical)
	if err != nil {
		return codexSealableObservation{}, err
	}
	if codexVerifySealableObservation(ctx, observation) != nil {
		return codexSealableObservation{}, errCodexStateFile
	}
	return observation, nil
}

// Resume compares newly captured physical/source/reference material against the
// exact previously protected values; it never adopts a new source or reseals it.
func codexCompareSealableObservation(expected, current codexSealableObservation) error {
	if !reflect.DeepEqual(expected.Physical.State, current.Physical.State) || !reflect.DeepEqual(expected.Source, current.Source) || expected.Owned != current.Owned {
		return errCodexStateFile
	}
	return nil
}

// Retain this bounded observation through subsequent protected-record reads,
// then call this metadata/reference-only barrier immediately before publication.
func codexVerifySealableObservation(ctx context.Context, observed codexSealableObservation) error {
	if codexVerifyPhysicalObservation(ctx, observed.Physical) != nil || codexVerifySourceReferences(ctx, observed.Source) != nil || codexVerifyOwnedReferences(ctx, observed.Physical.Layout.Home, observed.Source, observed.Owned) != nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}
