package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallPublicationReplaysOriginalVersionAfterIndexWindow(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	options := InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "package"), DataRoot: filepath.Join(root, "data"), Version: "1.0.0"}
	interrupted := errors.New("owned-publication-interruption")
	checkpoints := 0
	_, err := installPackage(options, func(point string) error {
		if point == "version-published" {
			checkpoints++
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || checkpoints != 1 {
		t.Fatalf("did not reach original version-to-index publication window: %v (%d)", err, checkpoints)
	}
	versionRoot := filepath.Join(options.DestinationRoot, "versions", options.Version)
	original, err := os.Lstat(versionRoot)
	if err != nil || !original.IsDir() {
		t.Fatal("original stage was not actually published", err)
	}
	manifestPath := filepath.Join(versionRoot, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(options.DestinationRoot, "manifests", "1.0.0.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interruption must precede index publication", err)
	}
	replayed, err := InstallPackage(options)
	if err != nil {
		t.Fatalf("exact request must finish the original sealed publication: %v", err)
	}
	after, err := os.Lstat(versionRoot)
	if err != nil || !os.SameFile(original, after) {
		t.Fatal("replay replaced the original version directory", err)
	}
	actual, err := os.ReadFile(manifestPath)
	if err != nil || !reflect.DeepEqual(actual, manifestBytes) {
		t.Fatal("replay rebuilt the sealed manifest", err)
	}
	if replayed.VersionRoot != versionRoot || replayed.ManifestPath != manifestPath || len(replayed.Files) == 0 || replayed.ManifestSHA256 == "" {
		t.Fatalf("committed original result missing: %#v", replayed)
	}
	if err := verifyInstalledVersion(options.DestinationRoot, options.Version); err != nil {
		t.Fatal("replayed index did not commit the original version", err)
	}
}

func publicationFixture(t *testing.T) InstallOptions {
	t.Helper()
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	return InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "package"), DataRoot: filepath.Join(root, "data"), Version: "1.0.0"}
}

func publicationInterrupt(t *testing.T, options InstallOptions, point string) installIntent {
	t.Helper()
	interrupted := errors.New("owned-publication-interruption")
	_, err := installPackage(options, func(actual string) error {
		if actual == point {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("checkpoint %s not reached: %v", point, err)
	}
	body, err := os.ReadFile(filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var intent installIntent
	if err = json.Unmarshal(body, &intent); err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestInstallPublicationRejectsReplacedParentWithOriginalFile(t *testing.T) {
	for _, point := range []string{"intent-published", "version-published"} {
		t.Run(point, func(t *testing.T) {
			options := publicationFixture(t)
			intent := publicationInterrupt(t, options, point)
			name := options.Version
			if point == "intent-published" {
				name = intent.Stage
			}
			root := filepath.Join(options.DestinationRoot, "versions", name)
			parent := filepath.Join(root, "bin")
			original, err := os.Stat(filepath.Join(parent, "codex-orchestrator"))
			if err != nil {
				t.Fatal(err)
			}
			// Keep every sealed file inode and byte, but replace its parent directory.
			moved := filepath.Join(t.TempDir(), "original-bin")
			if err = os.Rename(parent, moved); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(filepath.Join(moved, "codex-orchestrator"), filepath.Join(parent, "codex-orchestrator")); err != nil {
				t.Fatal(err)
			}
			if _, err = InstallPackage(options); !errors.Is(err, ErrInstallPublicationConflict) {
				t.Fatalf("foreign parent containing original file must conflict: %v", err)
			}
			after, err := os.Stat(filepath.Join(parent, "codex-orchestrator"))
			if err != nil || !os.SameFile(original, after) {
				t.Fatal("foreign tree was changed or deleted", err)
			}
			if _, err = os.Stat(filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json")); err != nil {
				t.Fatal("conflicting intent was discarded", err)
			}
		})
	}
}

func TestInstallPublicationPendingBlocksLifecycle(t *testing.T) {
	for _, operation := range []string{"doctor", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			options := publicationFixture(t)
			publicationInterrupt(t, options, "version-published")
			intentPath := filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json")
			before, _ := os.ReadFile(intentPath)
			if operation == "doctor" {
				report, err := Doctor(options.DestinationRoot)
				found := false
				for _, issue := range report.Issues {
					found = found || strings.HasPrefix(issue, "install_pending:"+options.Version)
				}
				if err != nil || report.Healthy || !found {
					t.Fatalf("Doctor must identify retained pending install: %#v %v", report, err)
				}
			} else {
				result, err := UninstallPackage(options.DestinationRoot, options.Version)
				if !errors.Is(err, ErrInstallPublicationPending) || result.Status != "install_pending" {
					t.Fatalf("Uninstall must retain pending install: %#v %v", result, err)
				}
			}
			after, err := os.ReadFile(intentPath)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("lifecycle operation modified pending evidence", err)
			}
		})
	}
}
