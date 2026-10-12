package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type initialPublicationCrashRequest struct {
	Root, Stage string
	Request     store.OriginalOwnerResumeRequest
	Binding     contract.OriginalOwnerBinding
}

func TestInitialOwnerPublicationCrashHelper(t *testing.T) {
	path := os.Getenv("INITIAL_PUBLICATION_CRASH_REQUEST")
	if path == "" {
		t.Skip("subprocess-only publication crash helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var request initialPublicationCrashRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(request.Root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope, owner := request.Binding.Scope, request.Binding.Owner
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("c", 32))).Public().(ed25519.PublicKey)
	obs := store.OriginalOwnerSuccessionObservation{OwnerID: scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: owner.PID, Birth: owner.Birth}, Host: request.Binding.FirstHost, Origin: store.RunOwnerBinding{OriginContextID: scope.OriginalOriginContextID, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth}, Epoch: 1, ExecutableSHA256: scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:01Z"}
	resolved, err := db.ResolveInitialOwnerRecovery(context.Background(), request.Request, obs, key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := OpenInitialOwnerRecoveryPublication(request.Root, resolved.RootAuthority, scope, owner, resolved.Proof)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	facts, err := lease.Facts()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := db.PrepareInitialOwnerRecovery(context.Background(), request.Request, obs, facts, key)
	if err != nil {
		t.Fatal(err)
	}
	lease.publicationHook = func(stage string, _ *os.File) error {
		if stage == request.Stage {
			if e := os.WriteFile(path+".ready", []byte(stage), 0600); e != nil {
				return e
			}
			for {
				time.Sleep(time.Second)
			}
		}
		return nil
	}
	if err = lease.PublishPrepared(proof); err != nil {
		t.Fatal(err)
	}
	t.Fatal("publication missed requested crash boundary")
}

// Real publisher SIGKILL and original Cmd.Wait supplement the five in-process
// seam checks. Owner death facts are deliberately synthetic here; the public
// CLI test independently proves actual authenticated Owner/Host death.
func TestInitialOwnerPublicationSurvivesPublisherSIGKILL(t *testing.T) {
	for _, stage := range []string{"before_publish", "after_publish"} {
		t.Run(stage, func(t *testing.T) {
			f := newInitialPublicationFixture(t)
			request := initialPublicationCrashRequest{Root: f.root, Stage: stage, Request: f.request, Binding: f.resolved.Binding}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.root, "publisher-crash.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestInitialOwnerPublicationCrashHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "INITIAL_PUBLICATION_CRASH_REQUEST="+path)
			var output strings.Builder
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-done
				}
				t.Logf("publisher PID=%d original Wait state=%v output=%s", cmd.Process.Pid, cmd.ProcessState, output.String())
			})
			for end := time.Now().Add(15 * time.Second); ; {
				if b, e := os.ReadFile(path + ".ready"); e == nil && string(b) == stage {
					break
				}
				select {
				case e := <-done:
					waited = true
					t.Fatal("publisher exited before crash", e, output.String())
				default:
				}
				if time.Now().After(end) {
					t.Fatal("publisher boundary timeout")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			waited = true
			var exited *exec.ExitError
			if !errors.As(err, &exited) {
				t.Fatal("publisher missing original SIGKILL Wait", err)
			}
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("publisher was not SIGKILL", status)
			}
			db, err := store.Open(filepath.Join(f.root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			f.db = db
			resolved, err := db.ResolveInitialOwnerRecovery(context.Background(), f.request, f.observation, f.key.Public().(ed25519.PublicKey))
			if err != nil || resolved.Proof == nil {
				t.Fatal("prepared proof lost", err)
			}
			lease := f.open(t, resolved.Proof)
			if err = lease.PublishPrepared(*resolved.Proof); err != nil {
				t.Fatal("exclusive publication replay", err)
			}
			if err = lease.ValidatePublished(); err != nil {
				t.Fatal(err)
			}
			facts, err := lease.Facts()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.FinalizeInitialOwnerRecovery(context.Background(), f.request, f.observation, facts, f.key.Public().(ed25519.PublicKey)); err != nil {
				t.Fatal("durable finalization after publisher death", err)
			}
			if err = lease.ValidatePublished(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
