package host

import (
	"context"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Only the original execution owns this object. Its live process observations
// and cleanup result are neither journal input nor a resumable capability.
type filterRuntime struct {
	grant                                 contract.LaunchCommand
	manifest                              contract.GitFilterManifestV1
	broker                                gitfilter.ResourceBroker
	opened                                bool
	providerAttempted                     bool
	provider                              *process.Handle
	providerStartedAt, providerTerminalAt time.Time
	cleanup                               *contract.GitFilterCleanup
	activeMS                              int64
}

func newFilterRuntime(g contract.LaunchCommand) (*filterRuntime, error) {
	if g.GitFilter == nil || contract.RequireGitFilter(g) != nil {
		return nil, contract.ErrGitFilterContract
	}
	m, err := contract.GitFilterManifestFromPayload(g.AdapterPayload)
	if err != nil || m == nil {
		return nil, contract.ErrGitFilterContract
	}
	g.AdapterPayload = append([]byte(nil), g.AdapterPayload...)
	a, f := *g.HostArtifacts, *g.GitFilter
	g.HostArtifacts, g.GitFilter = &a, &f
	if g.ValidationTool != nil {
		v := *g.ValidationTool
		g.ValidationTool = &v
	}
	return &filterRuntime{grant: g, manifest: m.Clone()}, nil
}

func (f *filterRuntime) open(ctx context.Context, sp *events.Spool) error {
	if f.opened {
		return errors.New("git_filter_runtime_reused")
	}
	f.opened = true
	var err error
	f.broker, err = gitfilter.OpenBroker(ctx, gitfilter.BrokerRequest{Launch: f.grant, Grant: *f.grant.GitFilter, Manifest: f.manifest, SegmentDirectory: sp.Directory(), Spool: sp})
	return err
}

func (f *filterRuntime) close() error {
	if f.broker == nil {
		return nil
	}
	return f.broker.Close()
}
