package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"sync/atomic"
	"syscall"
)

// Only the actual command-Start boundary creates this proof. Fields are private
// and JSON/error text cannot mint authority. It is bound to one live context
// and the exact wrapper command, including the independently pinned tool.
type neverStartedProof struct {
	consumed     atomic.Bool
	code, digest string
	ctx          context.Context
	cause        error
}

func (p *neverStartedProof) Error() string { return p.code }
func (p *neverStartedProof) Unwrap() error { return p.cause }
func commandProofHash(s Command) string {
	body, _ := json.Marshal(struct {
		Path                                               string
		Args                                               []string
		Dir                                                string
		Env                                                []string
		ExactEnv                                           bool
		Stdin                                              []byte
		PinnedPath, PinnedSHA256, SealedPath, SealedSHA256 string
	}{s.Path, s.Args, s.Dir, s.Env, s.ExactEnv, s.Stdin, s.PinnedPath, s.PinnedSHA256, s.SealedPath, s.SealedSHA256})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func neverStartedError(ctx context.Context, spec Command, cmd *exec.Cmd, err error) error {
	if cmd.Process != nil || cmd.ProcessState != nil || ctx.Err() != nil {
		return err
	}
	code := ""
	if errors.Is(err, syscall.EAGAIN) {
		code = "validation_start_eagain"
	} else if errors.Is(err, syscall.EINTR) {
		code = "validation_start_eintr"
	}
	if code == "" {
		return err
	}
	return &neverStartedProof{code: code, digest: commandProofHash(spec), ctx: ctx, cause: err}
}
func NeverStartedCode(ctx context.Context, err error, spec Command) string {
	var proof *neverStartedProof
	if ctx == nil || ctx.Err() != nil || !errors.As(err, &proof) || !reflect.ValueOf(ctx).Comparable() || !reflect.ValueOf(proof.ctx).Comparable() || proof.ctx != ctx || proof.digest != commandProofHash(spec) {
		return ""
	}
	return proof.code
}

// Trusted per-call boundary seam. Production Start always invokes cmd.Start.
// No global/environment/request selector exists; a callback which actually
// starts the process retains its original Handle/Wait even when returning error.
func StartWithCommandStarter(ctx context.Context, spec Command, starter func(*exec.Cmd) error) (*Handle, error) {
	if starter == nil {
		return Start(ctx, spec)
	}
	h, _, err := startCommandUsing(ctx, spec, false, processBirth, starter)
	return h, err
}

// Consume grants one live publication, even if its persistence later fails.
func ConsumeNeverStartedCode(ctx context.Context, err error, spec Command) string {
	code := NeverStartedCode(ctx, err, spec)
	if code == "" {
		return ""
	}
	var proof *neverStartedProof
	if !errors.As(err, &proof) || !proof.consumed.CompareAndSwap(false, true) {
		return ""
	}
	return code
}
