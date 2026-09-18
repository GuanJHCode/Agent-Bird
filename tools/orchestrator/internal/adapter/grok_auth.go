package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// RefreshGrokAuth runs only the pinned CLI's native model-catalog operation in
// the source context. The native client may renew its existing login; no model
// prompt, session, permission override, credential read or copy is performed by
// Bird. Workers still run under the unchanged filesystem sandbox afterwards.
func RefreshGrokAuth(ctx context.Context, pin BinaryPin, env []string) error {
	if err := VerifyExecutable(pin, pin.Version); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	environment := append(append([]string{}, env...), "GROK_DISABLE_AUTOUPDATER=1")
	p, err := process.Start(probeCtx, process.Command{Path: pin.Path, Args: []string{"models"}, Env: environment, PinnedPath: pin.Path, PinnedSHA256: pin.SHA256})
	if err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, errors.New("grok_auth_preflight_start_failed"))
	}
	waitErr := p.Wait(probeCtx)
	if waitErr != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		stopErr := p.Stop(stopCtx)
		stop()
		if stopErr != nil {
			return errors.Join(process.ErrProcessTreeUnknown, stopErr)
		}
	}
	if err := p.ConfirmTreeExited(); err != nil {
		return err
	}
	if waitErr != nil || p.ExitCode() != 0 {
		return errors.New("grok_auth_preflight_failed")
	}
	return nil
}
