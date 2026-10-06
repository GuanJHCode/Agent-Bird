package host

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOwnerRuntimeInitialIdentityUsesLiveProcessAndMemoryKey(t *testing.T) {
	fixture, _, _ := ownerEnrollmentFixture(t)
	b := fixture.bootstrap
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parentBirth, err := process.BirthContext(ctx, os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil {
		t.Fatal(err)
	}
	b.Prepared.Intent.FirstHost.PID = os.Getppid()
	b.Prepared.Intent.FirstHost.Birth = parentBirth
	b.Prepared.Intent.Scope.OriginalOriginPID = os.Getppid()
	b.Prepared.Intent.Scope.OriginalOriginBirth = parentBirth
	b.Prepared.Intent.Scope.Executable = executable
	b.Prepared.Intent.Scope.ExecutableSHA256 = digest
	if err = os.Chmod(b.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	e, err := newOwnerEnrollment(ctx, b)
	if err != nil || e == nil {
		t.Fatal("live enrollment constructor rejected", err)
	}
	birth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if e.client.Owner.PID != os.Getpid() || e.client.Owner.Birth != birth || e.client.Owner.PublicKey != hex.EncodeToString(e.client.PrivateKey.Public().(ed25519.PublicKey)) || e.client.Intent != b.Prepared.Intent || e.bootstrap.BootstrapToken != b.BootstrapToken {
		t.Fatal("owner identity was copied from transport or enrollment secret lost")
	}
	entries, err := os.ReadDir(b.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("identity initialization wrote private key or spool before authority", err)
	}
	for _, kind := range []string{"expired", "parent", "origin", "executable", "digest", "public_state", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			wrong := b
			childCtx, stop := context.WithCancel(context.Background())
			defer stop()
			switch kind {
			case "expired":
				wrong.Prepared.Intent.StartupDeadlineUnixMS = time.Now().Add(-time.Second).UnixMilli()
			case "parent":
				wrong.Prepared.Intent.FirstHost.PID++
			case "origin":
				wrong.Prepared.Intent.Scope.OriginalOriginBirth += "-foreign"
			case "executable":
				wrong.Prepared.Intent.Scope.Executable += ".foreign"
			case "digest":
				wrong.Prepared.Intent.Scope.ExecutableSHA256 = strings.Repeat("0", 64)
			case "public_state":
				if err := os.Chmod(b.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(b.StateDir, 0700)
			case "canceled":
				stop()
			}
			if got, err := newOwnerEnrollment(childCtx, wrong); err == nil || got != nil {
				t.Fatal("untrusted initial identity got owner key")
			}
		})
	}
}

func TestInitialTransportPinsSelfWithoutWeakeningInheritedParent(t *testing.T) {
	e, bound, _ := ownerEnrollmentFixture(t)
	b := contract.OwnerTransportBootstrap{Version: 1, StateDir: e.bootstrap.StateDir, Intent: e.bootstrap.Prepared.Intent, Owner: bound.Binding.Owner, CoordinatorPublicKey: e.bootstrap.Prepared.CoordinatorPublicKey}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	selfBirth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	parentBirth, err := process.BirthContext(ctx, os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil {
		t.Fatal(err)
	}
	b.Intent.FirstHost = contract.OriginalOwnerTransport{PID: os.Getpid(), Birth: selfBirth}
	b.Intent.Scope.OriginalOriginPID, b.Intent.Scope.OriginalOriginBirth = os.Getppid(), parentBirth
	b.Intent.Scope.Executable, b.Intent.Scope.ExecutableSHA256 = executable, digest
	if err := os.Chmod(b.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := initialTransportProcessIdentity(ctx, b); err != nil || got != b.Intent.FirstHost {
		t.Fatal("initial self was treated as a child", err)
	}
	if _, err := ownerProcessIdentity(ctx, b.StateDir, b.Intent.Scope, b.Intent.FirstHost); err == nil {
		t.Fatal("inherited role accepted itself as its parent")
	}
	if _, err := ownerProcessIdentity(ctx, b.StateDir, b.Intent.Scope, contract.OriginalOwnerTransport{PID: os.Getppid(), Birth: parentBirth}); err != nil {
		t.Fatal("valid inherited parent rejected", err)
	}
	for _, kind := range []string{"self_pid", "self_birth", "origin_birth", "executable", "digest", "public_state", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			wrong := b
			child, stop := context.WithCancel(ctx)
			defer stop()
			switch kind {
			case "self_pid":
				wrong.Intent.FirstHost.PID = os.Getppid()
			case "self_birth":
				wrong.Intent.FirstHost.Birth += "-foreign"
			case "origin_birth":
				wrong.Intent.Scope.OriginalOriginBirth += "-foreign"
			case "executable":
				wrong.Intent.Scope.Executable += ".foreign"
			case "digest":
				wrong.Intent.Scope.ExecutableSHA256 = strings.Repeat("0", 64)
			case "public_state":
				if err := os.Chmod(b.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(b.StateDir, 0700)
			case "canceled":
				stop()
			}
			if _, err := initialTransportProcessIdentity(child, wrong); err == nil {
				t.Fatal("initial transport identity guard bypassed")
			}
		})
	}
}
