package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// runProtocol2NativeFixture preserves the production CLI route for public
// operations and original-owner.  Only source-host installation is replaced so
// the test can retain the original FirstHost Cmd.Wait and kill that FirstHost
// after its signed Start acknowledgement but before it writes owner payload.
func runProtocol2NativeFixture() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "public-succession-origin":
		err = runPublicRebindOrigin()
	case "source-host":
		err = publicSuccessionOwnedHostLauncher(os.Args[2:])
	case "public-succession-owned-host":
		if trigger := os.Getenv("OWNER_LAUNCH_PROTOCOL2_PREBIND_TRIGGER"); trigger != "" {
			err = protocol2PreBindOwnedHost(os.Args[2:], trigger)
		} else {
			err = publicSuccessionOwnedHost(os.Args[2:])
		}
	default:
		err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, `{"version":1,"status":"error","error":%q}`+"\n", errorCode(err))
		os.Exit(2)
	}
}

func protocol2PreBindOwnedHost(args []string, trigger string) error {
	previous := ownerLaunchBeforePayload
	defer func() { ownerLaunchBeforePayload = previous }()
	ownerLaunchBeforePayload = func(_ context.Context, bootstrap contract.OwnerRuntimeBootstrap) error {
		if _, err := os.Stat(trigger); err != nil {
			return nil // only the first attempt may kill itself
		}
		marker := struct {
			OwnerID     string `json:"owner_id"`
			AttemptHash string `json:"attempt_sha256"`
			Incarnation uint64 `json:"incarnation"`
		}{bootstrap.Prepared.Intent.Scope.OwnerID, bootstrap.Prepared.LaunchAttempt.AttemptSHA256, bootstrap.Prepared.Intent.Scope.Incarnation}
		raw, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		if err = os.WriteFile(trigger+".marker", append(raw, '\n'), 0600); err != nil {
			return err
		}
		if err = os.Remove(trigger); err != nil {
			return err
		}
		return syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	if err := publicSuccessionOwnedHost(args); err != nil && !errors.Is(err, syscall.EINTR) {
		return err
	}
	return nil
}

// TestPublicOwnerLaunchProtocol2PreBindKill is completed once the Store-side
// pre-Bind retry-admission API lands.  The fixture above is intentionally kept
// separate so its process routing can be reviewed without production changes.
