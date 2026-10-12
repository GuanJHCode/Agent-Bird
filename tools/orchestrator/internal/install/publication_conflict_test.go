package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallPublicationExclusivePrimitiveRetainsForeignTarget(t *testing.T) {
	for _, kind := range []string{"empty-directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			options := publicationFixture(t)
			i := publicationInterrupt(t, options, "intent-published")
			a, err := openPublicationAnchors(options.DestinationRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer a.close()
			target := filepath.Join(options.DestinationRoot, "versions", options.Version)
			foreign := filepath.Join(t.TempDir(), "foreign")
			if err = os.WriteFile(foreign, []byte("foreign-content"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "empty-directory":
				err = os.Mkdir(target, 0700)
			case "file":
				err = os.WriteFile(target, []byte("foreign-content"), 0600)
			case "symlink":
				err = os.Symlink(foreign, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(target)
			stageBefore, _ := os.Stat(filepath.Join(options.DestinationRoot, "versions", i.Stage))
			if err = a.publishStage(i.Stage, options.Version, i.Directory); err == nil {
				t.Fatal("no-replace syscall replaced foreign target")
			}
			after, _ := os.Lstat(target)
			stageAfter, _ := os.Stat(filepath.Join(options.DestinationRoot, "versions", i.Stage))
			if !os.SameFile(before, after) || !os.SameFile(stageBefore, stageAfter) {
				t.Fatal("foreign target or original stage changed")
			}
			body, _ := os.ReadFile(foreign)
			if string(body) != "foreign-content" {
				t.Fatal("symlink target modified")
			}
			if err = os.WriteFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"), []byte("foreign-index"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = a.publishRecord(a.manifests, options.Version+".json", []byte("replacement-index")); err == nil {
				t.Fatal("index overwritten")
			}
			body, _ = os.ReadFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"))
			if string(body) != "foreign-index" {
				t.Fatal("foreign index bytes changed")
			}
		})
	}
}

func TestInstallPublicationRejectsChangedParentAnchor(t *testing.T) {
	options := publicationFixture(t)
	i := publicationInterrupt(t, options, "intent-published")
	a, err := openPublicationAnchors(options.DestinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	original := filepath.Join(options.DestinationRoot, "versions")
	moved := original + "-original"
	if err = os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err = a.publishStage(i.Stage, options.Version, i.Directory); err == nil {
		t.Fatal("stale parent FD accepted")
	}
	if _, err = os.Stat(filepath.Join(moved, i.Stage)); err != nil {
		t.Fatal("original anchored stage moved", err)
	}
	entries, _ := os.ReadDir(original)
	if len(entries) != 0 {
		t.Fatal("replacement public parent modified")
	}
}

func TestInstallPublicationForeignTreeAndRecordConflicts(t *testing.T) {
	changes := []string{"file-bytes", "file-inode", "file-mode", "symlink", "hardlink", "manifest-bytes", "root-inode", "both-trees", "neither-tree", "intent-partial", "intent-duplicate", "intent-extra", "index-partial", "both-cleanup-names", "foreign-cleanup"}
	for _, change := range changes {
		t.Run(change, func(t *testing.T) {
			options := publicationFixture(t)
			point := "version-published"
			if change == "both-cleanup-names" || change == "foreign-cleanup" {
				point = "before-intent-cleanup"
			}
			i := publicationInterrupt(t, options, point)
			root := filepath.Join(options.DestinationRoot, "versions", options.Version)
			file := filepath.Join(root, "bin", "codex-orchestrator")
			intentPath := filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json")
			intentBody, _ := os.ReadFile(intentPath)
			var preservedPath string
			var preservedBytes []byte
			switch change {
			case "file-bytes":
				if err := os.WriteFile(file, []byte("changed"), 0700); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				if err := os.Chmod(file, 0600); err != nil {
					t.Fatal(err)
				}
			case "file-inode":
				body, _ := os.ReadFile(file)
				if err := os.Rename(file, file+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, body, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				preservedPath = filepath.Join(t.TempDir(), "foreign-file")
				preservedBytes = []byte("foreign-file")
				if err := os.WriteFile(preservedPath, preservedBytes, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(preservedPath, file); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(t.TempDir(), "linked-file")); err != nil {
					t.Fatal(err)
				}
			case "manifest-bytes":
				if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "root-inode":
				moved := filepath.Join(t.TempDir(), "original-tree")
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				entries, _ := os.ReadDir(moved)
				for _, e := range entries {
					if err := os.Rename(filepath.Join(moved, e.Name()), filepath.Join(root, e.Name())); err != nil {
						t.Fatal(err)
					}
				}
			case "both-trees":
				if err := os.Mkdir(filepath.Join(options.DestinationRoot, "versions", i.Stage), 0700); err != nil {
					t.Fatal(err)
				}
			case "neither-tree":
				if err := os.Rename(root, root+"-retained"); err != nil {
					t.Fatal(err)
				}
			case "intent-partial":
				if err := os.WriteFile(intentPath, []byte("{\"schema_version\":1}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "intent-duplicate":
				if err := os.WriteFile(intentPath, append([]byte(`{"schema_version":1,`), intentBody[1:]...), 0600); err != nil {
					t.Fatal(err)
				}
			case "intent-extra":
				if err := os.WriteFile(intentPath, append([]byte(`{"unexpected":0,`), intentBody[1:]...), 0600); err != nil {
					t.Fatal(err)
				}
			case "index-partial":
				if err := os.WriteFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"), []byte(`{"version":"1.0.0","manifest_sha256":"`+i.ManifestSHA256+`","request_sha256":"`+i.RequestSHA256+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "both-cleanup-names", "foreign-cleanup":
				cleanup := filepath.Join(options.DestinationRoot, "install-intents", publicationCleanupName(options.Version, publicationBytesDigest(intentBody)))
				preservedPath = cleanup
				preservedBytes = []byte("foreign-cleanup")
				if change == "both-cleanup-names" {
					preservedBytes = intentBody
				}
				if err := os.WriteFile(cleanup, preservedBytes, 0600); err != nil {
					t.Fatal(err)
				}
				if change == "foreign-cleanup" {
					if err := os.Remove(intentPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := InstallPackage(options); !errors.Is(err, ErrInstallPublicationConflict) {
				t.Fatalf("conflict %s admitted: %v", change, err)
			}
			if preservedPath != "" {
				body, err := os.ReadFile(preservedPath)
				if err != nil || !bytes.Equal(body, preservedBytes) {
					t.Fatal("foreign evidence changed", err)
				}
			}
			if change != "foreign-cleanup" {
				if _, err := os.Lstat(intentPath); err != nil {
					t.Fatal("intent deleted", err)
				}
			}
		})
	}
}

func TestInstallPublicationCommittedParentSwapBlocksRemoval(t *testing.T) {
	options := publicationFixture(t)
	result, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(result.VersionRoot, "bin")
	moved := filepath.Join(t.TempDir(), "original-bin")
	if err = os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "codex-orchestrator")
	if err = os.Rename(filepath.Join(moved, "codex-orchestrator"), file); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	if err = PinVersion(options.DestinationRoot, "must-not-pin", options.Version); err == nil {
		t.Fatal("foreign parent pinned")
	}
	report, err := Doctor(options.DestinationRoot)
	if err != nil || report.Healthy {
		t.Fatal("foreign parent healthy", err)
	}
	if _, err = UninstallPackage(options.DestinationRoot, options.Version); !errors.Is(err, ErrChangedFiles) {
		t.Fatal("foreign parent uninstalled", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign parent or original file removed", err)
	}
}

func TestInstallPublicationPendingUninstallBlocksReplay(t *testing.T) {
	options := publicationFixture(t)
	result, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	value, err := readManifest(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := uninstallIntent{SchemaVersion: 1, Version: options.Version, ManifestSHA256: result.ManifestSHA256, Manifest: value, CreatedAt: "2026-10-05T00:00:00Z"}
	body, _ := json.Marshal(intent)
	name := filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json")
	if err = os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = InstallPackage(options); !errors.Is(err, ErrInstallPublicationConflict) {
		t.Fatal("pending uninstall admitted", err)
	}
	after, _ := os.ReadFile(name)
	if !bytes.Equal(body, after) {
		t.Fatal("pending uninstall changed")
	}
}

func TestInstallPublicationConcurrentCallsShareOneCommittedTree(t *testing.T) {
	options := publicationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	type answer struct {
		result InstallResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := installPackage(options, func(point string) error {
			if point == "intent-published" {
				close(entered)
				<-release
			}
			return nil
		})
		done <- answer{result, err}
	}()
	<-entered
	_, err := InstallPackage(options)
	close(release)
	first := <-done
	if err == nil || err.Error() != "install_locked" || first.err != nil {
		t.Fatalf("cooperative exclusion failed: %v %v", err, first.err)
	}
	second, err := InstallPackage(options)
	if err != nil || !reflect.DeepEqual(first.result, second) {
		t.Fatalf("replay identity differs: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(options.DestinationRoot, "versions"))
	if len(entries) != 1 || entries[0].Name() != options.Version {
		t.Fatalf("duplicate tree: %v", entries)
	}
}

func TestInstallPublicationUnknownCleanupBlocksOnlyRelatedRemoval(t *testing.T) {
	for _, kind := range []string{"different-digest", "unrelated-version", "invalid-name", "directory-entry"} {
		t.Run(kind, func(t *testing.T) {
			options := publicationFixture(t)
			if _, err := InstallPackage(options); err != nil {
				t.Fatal(err)
			}
			version := options.Version
			if kind == "unrelated-version" {
				version = "9.9.9"
			}
			name := publicationCleanupName(version, strings.Repeat("a", 64))
			if kind == "invalid-name" {
				name = ".cleanup-unclassifiable.json"
			}
			path := filepath.Join(options.DestinationRoot, "install-intents", name)
			if kind == "directory-entry" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("foreign-cleanup"), 0600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			result, err := UninstallPackage(options.DestinationRoot, options.Version)
			if kind == "unrelated-version" {
				if err != nil || result.Status != "removed" {
					t.Fatalf("unrelated version blocked removal: %#v %v", result, err)
				}
			} else if kind == "different-digest" {
				if !errors.Is(err, ErrInstallPublicationPending) || result.Status != "install_pending" {
					t.Fatalf("same-version unknown cleanup did not block: %#v %v", result, err)
				}
			} else if !errors.Is(err, ErrInstallPublicationConflict) || result.Status != "retained" {
				t.Fatalf("unclassifiable record did not block: %#v %v", result, err)
			}
			after, _ := os.Lstat(path)
			if !os.SameFile(before, after) {
				t.Fatal("unknown cleanup adopted or removed")
			}
		})
	}
}

func TestInstallPublicationCleanupRechecksMovedOriginal(t *testing.T) {
	options := publicationFixture(t)
	foreign := []byte("foreign-replacement")
	var path string
	_, err := installPackage(options, func(point string) error {
		if point != "intent-cleanup-renamed" {
			return nil
		}
		entries, err := os.ReadDir(filepath.Join(options.DestinationRoot, "install-intents"))
		if err != nil || len(entries) != 1 {
			t.Fatal(err)
		}
		path = filepath.Join(options.DestinationRoot, "install-intents", entries[0].Name())
		if err = os.Rename(path, filepath.Join(t.TempDir(), "original-cleanup")); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(path, foreign, 0600)
	})
	if !errors.Is(err, ErrInstallPublicationConflict) {
		t.Fatalf("moved-record replacement accepted: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, foreign) {
		t.Fatal("foreign cleanup removed", err)
	}
}
