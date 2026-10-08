//go:build !windows

package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type configCrashRequest struct {
	Executable string                    `json:"executable"`
	Scope      string                    `json:"scope"`
	Operation  ConfigGenerationOperation `json:"operation"`
	Checkpoint string                    `json:"checkpoint"`
	Ready      string                    `json:"ready"`
}

type configCrashEntry struct {
	Directory publicationDirectoryIdentity `json:"directory"`
	File      *publicationFileIdentity     `json:"file,omitempty"`
	SHA256    string                       `json:"sha256,omitempty"`
	Mtime     int64                        `json:"mtime"`
}

type configCrashReady struct {
	PID           int                         `json:"pid"`
	Checkpoint    string                      `json:"checkpoint"`
	RequestSHA256 string                      `json:"request_sha256"`
	Files         map[string]configCrashEntry `json:"files"`
}

// Only the test entry reads this private fixture request. Production APIs have
// no failure environment or hook. The helper never replays or launches children.
func TestConfigGenerationPublicationCrashHelper(t *testing.T) {
	requestPath := os.Getenv("ORCHESTRATOR_TEST_CONFIG_GENERATION_CRASH_REQUEST")
	if requestPath == "" {
		t.Skip("child process helper")
	}
	started := time.Now()
	raw, err := readOwnedRegular(requestPath, 0600, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	var request configCrashRequest
	if generationDecode(raw, &request, 16<<10) != nil || request.Executable == "" || request.Ready == "" || !generationHash(request.Scope) {
		t.Fatal("invalid private crash request")
	}
	validPoint := request.Checkpoint == "intent" || request.Checkpoint == "manifest" || request.Checkpoint == "active" || request.Checkpoint == "receipt"
	if !validPoint {
		t.Fatal("invalid crash checkpoint")
	}
	_, err = updateConfigGeneration(request.Executable, request.Scope, request.Operation, func(point string) error {
		if point != request.Checkpoint {
			return nil
		}
		identity, err := runningPackageIdentity(request.Executable)
		if err != nil {
			return err
		}
		ready := configCrashReady{PID: os.Getpid(), Checkpoint: point, RequestSHA256: publicationBytesDigest(raw), Files: configCrashTree(t, identity.DestinationRoot)}
		body, err := json.Marshal(ready)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(request.Ready, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		n, writeErr := f.Write(append(body, '\n'))
		if writeErr != nil || n != len(body)+1 {
			_ = f.Close()
			return errors.New("crash ready short write")
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(request.Ready)); err != nil {
			return err
		}
		remaining := time.Until(started.Add(20 * time.Second))
		if remaining <= 0 {
			return errors.New("original crash deadline expired")
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		<-timer.C
		return errors.New("original crash deadline expired")
	})
	t.Fatalf("helper returned instead of original SIGKILL: %v", err)
}

// Capture bytes and physical identities, excluding atime (reads legitimately
// affect it). Directory mtime/inode and every ordinary file identity are included.
func configCrashTree(t *testing.T, root string) map[string]configCrashEntry {
	t.Helper()
	out := map[string]configCrashEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dir, err := publicationDirectory(info)
		if err != nil {
			return err
		}
		value := configCrashEntry{Directory: dir, Mtime: info.ModTime().UnixNano()}
		if !entry.IsDir() {
			id, err := publicationFile(info)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.File = &id
			value.SHA256 = publicationBytesDigest(raw)
		}
		out[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func configCrashTreeDiff(before, after map[string]configCrashEntry) string {
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			return fmt.Sprintf("removed %s: %#v", name, want)
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("changed %s: before=%#v after=%#v", name, want, got)
		}
	}
	for name, got := range after {
		if _, ok := before[name]; !ok {
			return fmt.Sprintf("created %s: %#v", name, got)
		}
	}
	return "unknown tree difference"
}

type configCrashFixture struct {
	root, destination, executable, scope string
	initial                              ConfigGenerationReceipt
	operation                            ConfigGenerationOperation
}

func newConfigCrashFixture(t *testing.T) configCrashFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "config-generation-crash-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("retained task-owned prefix:", root)
		} else if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	_, err = InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dest, "versions/1.0.0/bin/codex-orchestrator")
	scope, err := contract.ConfigGenerationOwnerScope("crash-controller", "original-origin-birth")
	if err != nil {
		t.Fatal(err)
	}
	initial := ownedConfigPublish(t, executable, scope, "original-a", nil, "a")
	values := ConfigSavedDefaults{AGY: "b", Claude: "claude-b", Codex: ""}
	return configCrashFixture{root, dest, executable, scope, initial, ConfigGenerationOperation{RequestID: "original-b", Action: "publish", Expected: initial.Active, SavedDefaults: &values}}
}

func (f configCrashFixture) crash(t *testing.T, point string) configCrashReady {
	t.Helper()
	request := configCrashRequest{f.executable, f.scope, f.operation, point, filepath.Join(f.root, "ready.json")}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	requestPath := filepath.Join(f.root, "crash-request.json")
	if err = os.WriteFile(requestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("original helper binary sha256=%x", sha256.Sum256(image))
	command := exec.Command(os.Args[0], "-test.run=^TestConfigGenerationPublicationCrashHelper$", "-test.timeout=30s", "-test.v")
	command.Env = append(os.Environ(), "ORCHESTRATOR_TEST_CONFIG_GENERATION_CRASH_REQUEST="+requestPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			select {
			case waitErr := <-done:
				waited = true
				t.Errorf("forced helper cleanup PID=%d Wait=%v", command.Process.Pid, waitErr)
			case <-time.After(3 * time.Second):
				t.Error("forced helper cleanup original Wait timeout")
			}
		}
		t.Logf("raw original helper PID=%d:\n%s", command.Process.Pid, output.String())
	})
	var ready configCrashReady
	deadline := time.Now().Add(10 * time.Second)
	for {
		body, readErr := os.ReadFile(request.Ready)
		if readErr == nil && json.Unmarshal(body, &ready) == nil {
			break
		}
		select {
		case waitErr := <-done:
			waited = true
			t.Fatal("original helper exited before checkpoint", waitErr, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("original helper checkpoint timeout", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ready.PID != command.Process.Pid || ready.Checkpoint != point || ready.RequestSHA256 != publicationBytesDigest(raw) || !reflect.DeepEqual(ready.Files, configCrashTree(t, f.destination)) {
		t.Fatal("ready did not bind original process/request/actual tree")
	}
	// Prove the real syscall lock is still held before terminating its owner.
	lock, lockErr := acquireInstallLock(f.destination)
	if lockErr == nil {
		releaseInstallLock(lock)
		t.Fatal("live child did not hold install lock")
	}
	if lockErr.Error() != "install_locked" {
		t.Fatal("unexpected live lock refusal", lockErr)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case waitErr := <-done:
		waited = true
		var exited *exec.ExitError
		if !errors.As(waitErr, &exited) || command.ProcessState == nil {
			t.Fatal("missing actual crash Wait", waitErr)
		}
		status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatal("original Wait was not SIGKILL", waitErr, command.ProcessState)
		}
		t.Logf("original Cmd PID=%d checkpoint=%s Wait=%v signal=%s", command.Process.Pid, point, waitErr, status.Signal())
	case <-time.After(3 * time.Second):
		t.Fatal("original SIGKILL Wait timeout")
	}
	return ready
}

func TestConfigGenerationPublicationProcessCrashReplay(t *testing.T) {
	for _, point := range []string{"intent", "manifest", "active", "receipt"} {
		t.Run(point, func(t *testing.T) {
			f := newConfigCrashFixture(t)
			ready := f.crash(t, point)
			if _, err := ReadConfigGenerationActive(f.executable, f.scope); !errors.Is(err, ErrConfigGenerationPending) {
				t.Fatal("crashed publication lost pending fence", err)
			}
			before := configCrashTree(t, f.destination)
			doctor, err := Doctor(f.destination)
			if err != nil || doctor.Healthy {
				t.Fatal("Doctor did not report pending", doctor, err)
			}
			if !reflect.DeepEqual(before, configCrashTree(t, f.destination)) {
				t.Fatal("Doctor changed actual installation tree")
			}
			if _, err := UninstallPackage(f.destination, "1.0.0"); err == nil {
				t.Fatal("pending publication permitted uninstall")
			}
			if !reflect.DeepEqual(before, configCrashTree(t, f.destination)) {
				t.Fatal("pending uninstall changed actual installation tree")
			}
			// An old immutable receipt cannot complete a different pending request.
			old := ownedConfigPublish(t, f.executable, f.scope, "original-a", nil, "a")
			if !reflect.DeepEqual(old, f.initial) {
				t.Fatal("old receipt drifted")
			}
			if !reflect.DeepEqual(before, configCrashTree(t, f.destination)) {
				t.Fatal("old receipt replay changed crashed publication")
			}
			recovered, err := UpdateConfigGeneration(f.executable, f.scope, f.operation)
			if err != nil || recovered.Active == nil || recovered.Active.Revision != 2 || recovered.Ref == nil {
				t.Fatal("original replay failed", recovered, err)
			}
			active, err := ReadConfigGenerationActive(f.executable, f.scope)
			if err != nil || !reflect.DeepEqual(active, recovered.Active) {
				t.Fatal("active differs from original receipt", active, err)
			}
			manifest, err := ReadConfigGeneration(f.executable, *recovered.Ref)
			if err != nil || !reflect.DeepEqual(manifest.SavedDefaults, *f.operation.SavedDefaults) {
				t.Fatal("replayed original manifest differs", manifest, err)
			}
			after := configCrashTree(t, f.destination)
			for name, entry := range ready.Files {
				if entry.File == nil {
					continue
				}
				preserve := strings.Contains(name, "/generations/") || strings.Contains(name, "/requests/") || (strings.HasSuffix(name, "/active.json") && (point == "active" || point == "receipt"))
				if preserve && !reflect.DeepEqual(entry, after[name]) {
					t.Fatal("replay rebuilt an original sealed file", name, entry, after[name])
				}
			}
			again, err := UpdateConfigGeneration(f.executable, f.scope, f.operation)
			if err != nil || !reflect.DeepEqual(again, recovered) || !reflect.DeepEqual(after, configCrashTree(t, f.destination)) {
				t.Fatal("completed replay is not immutable", again, err)
			}
			doctor, err = Doctor(f.destination)
			if err != nil || !doctor.Healthy {
				t.Fatal("recovered installation not healthy", doctor, err)
			}
			if !reflect.DeepEqual(after, configCrashTree(t, f.destination)) {
				t.Fatal("healthy Doctor changed actual tree")
			}
		})
	}
}

func TestConfigGenerationPublicationProcessCrashConflicts(t *testing.T) {
	for _, kind := range []string{"changed_request", "foreign_scope_expected", "active_inode_drift", "malformed_stage"} {
		t.Run(kind, func(t *testing.T) {
			f := newConfigCrashFixture(t)
			point := "intent"
			if kind == "malformed_stage" {
				point = "active"
			}
			f.crash(t, point)
			request, scope := f.operation, f.scope
			ownerRoot := filepath.Join(f.destination, "owned-config", scope)
			switch kind {
			case "changed_request":
				changed := *request.SavedDefaults
				changed.AGY = "different"
				request.SavedDefaults = &changed
			case "foreign_scope_expected":
				var err error
				scope, err = contract.ConfigGenerationOwnerScope("other-controller", "other-birth")
				if err != nil {
					t.Fatal(err)
				}
			case "active_inode_drift":
				path := filepath.Join(ownerRoot, "active.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path+".foreign", raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path+".foreign", path); err != nil {
					t.Fatal(err)
				}
			case "malformed_stage":
				if err := os.WriteFile(filepath.Join(ownerRoot, "stage.json"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := configCrashTree(t, f.destination)
			_, err := UpdateConfigGeneration(f.executable, scope, request)
			if err == nil {
				t.Fatal("conflicting crash replay accepted", kind)
			}
			if kind == "changed_request" || kind == "active_inode_drift" {
				if !errors.Is(err, ErrConfigGenerationConflict) {
					t.Fatal("wrong conflict result", err)
				}
			}
			after := configCrashTree(t, f.destination)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected replay changed preserved evidence", kind, err, configCrashTreeDiff(before, after))
			}
			t.Logf("exact conflict kind=%s error=%v", kind, err)
		})
	}
}
