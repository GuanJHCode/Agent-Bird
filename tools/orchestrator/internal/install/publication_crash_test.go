package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ROOT launches and retains this exact test process. This test never launches a
// descendant, executes replay or removes evidence. The production API has no
// environment-controlled checkpoint.
func TestC11InstallPublicationCrashHelper(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_C11_INSTALL_CRASH_HELPER") != "1" || os.Getenv("ORCHESTRATOR_C11_INSTALL_CRASH_ONCE") != "1" {
		t.Skip("requires explicit ROOT-owned original process crash authorization")
	}
	started := time.Now()
	deadline := started.Add(25 * time.Second)
	request, options, requestHash, err := readInstallCrashRequest(os.Getenv("ORCHESTRATOR_C11_INSTALL_CRASH_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = installPackage(options, func(point string) error {
		if point != request.Checkpoint {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("install_crash_original_deadline_expired")
		}
		if err := writeInstallCrashReady(request, options, requestHash, deadline); err != nil {
			return err
		}
		// Consume only the original monotonic deadline, including all preparation.
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		<-timer.C
		return errors.New("install_crash_original_deadline_expired")
	})
	// A normally returned helper is a failure; only ROOT's retained original
	// Wait/-signal record can establish the intended interrupted process.
	t.Fatalf("crash helper returned without ROOT terminating original process: %v", err)
}

type installCrashRequest struct {
	SchemaVersion  int    `json:"schema_version"`
	InvocationID   string `json:"invocation_id"`
	Checkpoint     string `json:"checkpoint"`
	InstallRequest string `json:"install_request_file"`
	ReadyPath      string `json:"ready_file"`
}

type installCrashOptions struct {
	SourceRoot      string `json:"source_root"`
	BinaryPath      string `json:"binary_path"`
	DestinationRoot string `json:"destination_root"`
	DataRoot        string `json:"data_root"`
	Version         string `json:"version"`
}

func readInstallCrashRequest(name string) (installCrashRequest, InstallOptions, string, error) {
	var request installCrashRequest
	var options InstallOptions
	bad := errors.New("install_crash_request_invalid")
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return request, options, "", bad
	}
	if canonical, err := canonicalManagedPath(name); err != nil || canonical != name || verifyPrivateDirectory(filepath.Dir(name)) != nil {
		return request, options, "", bad
	}
	body, err := readOwnedRegular(name, 0600, 16<<10)
	if err != nil || strictJSON(body, &request) != nil || request.SchemaVersion != 1 || !idPattern.MatchString(request.InvocationID) {
		return request, options, "", bad
	}
	if request.Checkpoint != "intent-published" && request.Checkpoint != "version-published" && request.Checkpoint != "index-published" {
		return request, options, "", bad
	}
	for _, target := range []string{request.InstallRequest, request.ReadyPath} {
		if !filepath.IsAbs(target) || filepath.Clean(target) != target || filepath.Dir(target) != filepath.Dir(name) {
			return request, options, "", bad
		}
		if canonical, err := canonicalManagedPath(target); err != nil || canonical != target {
			return request, options, "", bad
		}
	}
	if request.ReadyPath == request.InstallRequest || request.ReadyPath == name {
		return request, options, "", bad
	}
	if _, err = os.Lstat(request.ReadyPath); !errors.Is(err, os.ErrNotExist) {
		return request, options, "", bad
	}
	body, err = readOwnedRegular(request.InstallRequest, 0600, 16<<10)
	var wire installCrashOptions
	if err != nil || strictJSON(body, &wire) != nil {
		return request, options, "", bad
	}
	options = InstallOptions{SourceRoot: wire.SourceRoot, BinaryPath: wire.BinaryPath, DestinationRoot: wire.DestinationRoot, DataRoot: wire.DataRoot, Version: wire.Version}
	if validateOptions(options) != nil {
		return request, options, "", bad
	}
	for _, path := range []string{options.SourceRoot, options.BinaryPath, options.DestinationRoot, options.DataRoot} {
		if canonical, err := canonicalManagedPath(path); err != nil || canonical != path {
			return request, options, "", bad
		}
	}
	return request, options, publicationBytesDigest(body), nil
}

type installCrashReady struct {
	SchemaVersion        int                          `json:"schema_version"`
	InvocationID         string                       `json:"invocation_id"`
	OriginalPID          int                          `json:"original_pid_observation"`
	Checkpoint           string                       `json:"checkpoint"`
	OriginalDeadline     string                       `json:"original_deadline"`
	InstallRequestSHA256 string                       `json:"install_request_file_sha256"`
	RequestSHA256        string                       `json:"request_sha256"`
	IntentSHA256         string                       `json:"intent_sha256"`
	IntentIdentity       publicationFileIdentity      `json:"intent_file_identity"`
	TreeName             string                       `json:"tree_name"`
	TreeIdentity         publicationDirectoryIdentity `json:"tree_directory_identity"`
	ManifestSHA256       string                       `json:"manifest_sha256"`
	ManifestIdentity     publicationFileIdentity      `json:"manifest_file_identity"`
	Files                []manifestFile               `json:"files"`
	Directories          []manifestDirectory          `json:"directories"`
	IndexPresent         bool                         `json:"index_present"`
	IndexSHA256          string                       `json:"index_sha256,omitempty"`
	IndexIdentity        *publicationFileIdentity     `json:"index_file_identity,omitempty"`
}

func writeInstallCrashReady(request installCrashRequest, options InstallOptions, requestHash string, deadline time.Time) error {
	a, err := openPublicationAnchors(options.DestinationRoot)
	if err != nil {
		return err
	}
	defer a.close()
	i, body, _, identity, exists, err := a.readIntent(options.Version, nil)
	if err != nil || !exists {
		return errors.New("install_crash_actual_intent_unavailable")
	}
	name := options.Version
	if request.Checkpoint == "intent-published" {
		name = i.Stage
	}
	if _, err = a.validateSealed(i, name); err != nil {
		return err
	}
	var value manifest
	if err = strictJSONBounded(i.Manifest, &value, false, 4<<20); err != nil {
		return err
	}
	ready := installCrashReady{SchemaVersion: 1, InvocationID: request.InvocationID, OriginalPID: os.Getpid(), Checkpoint: request.Checkpoint, OriginalDeadline: deadline.UTC().Format(time.RFC3339Nano), InstallRequestSHA256: requestHash, RequestSHA256: i.RequestSHA256, IntentSHA256: publicationBytesDigest(body), IntentIdentity: identity, TreeName: name, TreeIdentity: i.Directory, ManifestSHA256: i.ManifestSHA256, ManifestIdentity: i.ManifestIdentity, Files: value.Files, Directories: value.Directories}
	ready.IndexPresent, err = publicationExists(a.manifests, options.Version+".json")
	if err != nil || ready.IndexPresent != (request.Checkpoint == "index-published") {
		return errors.New("install_crash_actual_index_mismatch")
	}
	if ready.IndexPresent {
		index, id, err := publicationReadAt(a.manifests, options.Version+".json", 4096)
		if err != nil {
			return err
		}
		actual, err := decodeManifestReceipt(index, options.Version)
		expected, _ := json.Marshal(receiptForInstall(i, ready.IntentSHA256))
		observed, _ := json.Marshal(actual)
		if err != nil || string(expected) != string(observed) {
			return errors.New("install_crash_actual_index_mismatch")
		}
		ready.IndexSHA256, ready.IndexIdentity = publicationBytesDigest(index), &id
	}
	encoded, err := json.Marshal(ready)
	if err != nil || len(encoded)+1 > installMaxRecord {
		return errors.New("install_crash_ready_limit")
	}
	parent, err := publicationOpenRoot(filepath.Dir(request.ReadyPath))
	if err != nil {
		return err
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		return errors.New("install_crash_ready_parent_unsafe")
	}
	if _, err = publicationWriteAt(parent, filepath.Base(request.ReadyPath), append(encoded, '\n')); err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		return err
	}
	return publicationSamePath(parent, filepath.Dir(request.ReadyPath))
}

func TestC11InstallCrashReadyBindsOriginalArtifacts(t *testing.T) {
	for _, point := range []string{"intent-published", "version-published", "index-published"} {
		t.Run(point, func(t *testing.T) {
			options := publicationFixture(t)
			readyParent := filepath.Join(filepath.Dir(options.SourceRoot), "private-ready")
			if err := os.Mkdir(readyParent, 0700); err != nil {
				t.Fatal(err)
			}
			request := installCrashRequest{SchemaVersion: 1, InvocationID: "owned-original-invocation", Checkpoint: point, ReadyPath: filepath.Join(readyParent, "ready.json")}
			interrupted := errors.New("pure-ready-stop")
			_, err := installPackage(options, func(actual string) error {
				if actual != point {
					return nil
				}
				if err := writeInstallCrashReady(request, options, strings.Repeat("a", 64), time.Now().Add(25*time.Second)); err != nil {
					return err
				}
				return interrupted
			})
			if !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
			body, err := readOwnedRegular(request.ReadyPath, 0600, installMaxRecord)
			if err != nil {
				t.Fatal(err)
			}
			var ready installCrashReady
			if json.Unmarshal(body, &ready) != nil || ready.OriginalPID != os.Getpid() || ready.InvocationID != request.InvocationID || ready.Checkpoint != point || len(ready.Files) == 0 || len(ready.Directories) == 0 || ready.IndexPresent != (point == "index-published") {
				t.Fatalf("ready did not bind real checkpoint: %#v", ready)
			}
			intent, _ := os.ReadFile(filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json"))
			if publicationBytesDigest(intent) != ready.IntentSHA256 {
				t.Fatal("ready substituted a plausible intent")
			}
			info, err := os.Stat(filepath.Join(options.DestinationRoot, "versions", ready.TreeName))
			if err != nil {
				t.Fatal(err)
			}
			id, _ := publicationDirectory(info)
			if id != ready.TreeIdentity {
				t.Fatal("ready original tree identity mismatch")
			}
			if err = writeInstallCrashReady(request, options, strings.Repeat("a", 64), time.Now().Add(25*time.Second)); !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing ready overwritten: %v", err)
			}
		})
	}
}

func TestC11InstallCrashRequestIsPrivateBoundedAndExclusive(t *testing.T) {
	for _, change := range []string{"valid", "unsupported-checkpoint", "unknown-field", "duplicate-field", "oversized", "public-mode", "existing-ready", "ready-outside", "install-symlink", "install-unknown"} {
		t.Run(change, func(t *testing.T) {
			options := publicationFixture(t)
			parent := filepath.Join(filepath.Dir(options.SourceRoot), "private-requests")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			requestPath, installPath, readyPath := filepath.Join(parent, "helper.json"), filepath.Join(parent, "install.json"), filepath.Join(parent, "ready.json")
			wire := installCrashOptions{SourceRoot: options.SourceRoot, BinaryPath: options.BinaryPath, DestinationRoot: options.DestinationRoot, DataRoot: options.DataRoot, Version: options.Version}
			installBody, _ := json.Marshal(wire)
			if change == "install-unknown" {
				installBody = append([]byte(`{"unexpected":0,`), installBody[1:]...)
			}
			if err := os.WriteFile(installPath, installBody, 0600); err != nil {
				t.Fatal(err)
			}
			request := installCrashRequest{SchemaVersion: 1, InvocationID: "original-one-shot", Checkpoint: "intent-published", InstallRequest: installPath, ReadyPath: readyPath}
			switch change {
			case "unsupported-checkpoint":
				request.Checkpoint = "cleanup"
			case "existing-ready":
				if err := os.WriteFile(readyPath, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ready-outside":
				request.ReadyPath = filepath.Join(t.TempDir(), "ready.json")
			case "install-symlink":
				request.InstallRequest = installPath + ".link"
				if err := os.Symlink(installPath, request.InstallRequest); err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(request)
			mode := os.FileMode(0600)
			switch change {
			case "unknown-field":
				body = append([]byte(`{"unexpected":0,`), body[1:]...)
			case "duplicate-field":
				body = append([]byte(`{"schema_version":1,`), body[1:]...)
			case "oversized":
				body = append(body, []byte(strings.Repeat(" ", 16<<10))...)
			case "public-mode":
				mode = 0644
			}
			if err := os.WriteFile(requestPath, body, mode); err != nil {
				t.Fatal(err)
			}
			read, actual, hash, err := readInstallCrashRequest(requestPath)
			if change == "valid" {
				if err != nil || actual != options || read != request || hash != publicationBytesDigest(installBody) {
					t.Fatal("valid original request binding", err)
				}
			} else if err == nil {
				t.Fatal("unsafe request admitted")
			}
			if change == "existing-ready" {
				actual, _ := os.ReadFile(readyPath)
				if string(actual) != "foreign" {
					t.Fatal("foreign ready overwritten")
				}
			}
			if _, err := os.Stat(options.DestinationRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("request validation modified install root", err)
			}
		})
	}
}
