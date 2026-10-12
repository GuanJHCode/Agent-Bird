package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallReviewFixUninstallTailReplaysMissingReceipt(t *testing.T) {
	options := publicationFixture(t)
	installed, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(options.DestinationRoot, "manifests")
	defer os.Chmod(parent, 0700)
	if err = os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	// The real public uninstall persists its intent and removes the original
	// tree, then cannot unlink the receipt in this owned read-only directory.
	first, err := UninstallPackage(options.DestinationRoot, options.Version)
	if err == nil || first.Status != "cleanup_pending" || fileExists(installed.VersionRoot) {
		t.Fatalf("did not reach the original uninstall tail: %#v %v", first, err)
	}
	intentPath := filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json")
	intent, exists, err := readUninstallIntent(intentPath, options.Version)
	if err != nil || !exists || intent.ManifestSHA256 != installed.ManifestSHA256 {
		t.Fatal("original public uninstall intent not retained", err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	// Model the interruption after the original index unlink, before intent
	// unlink. There is no hand-made uninstall intent or synthetic package actor.
	if err = os.Remove(filepath.Join(parent, options.Version+".json")); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(filepath.Dir(intentPath), "9.0.0.json")
	data := filepath.Join(options.DataRoot, "retained-session")
	if err = os.MkdirAll(options.DataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{unrelated, data} {
		if err = os.WriteFile(path, []byte("unrelated-owned-evidence"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := UninstallPackage(options.DestinationRoot, options.Version)
	if err != nil || result.Status != "removed" || fileExists(intentPath) {
		t.Fatalf("matching tail must finish after receipt removal: %#v %v", result, err)
	}
	for _, path := range []string{unrelated, data} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "unrelated-owned-evidence" {
			t.Fatal("tail cleanup changed unrelated data", err)
		}
	}
}

func TestInstallReviewFixMissingReceiptDoesNotDeleteOwnedTree(t *testing.T) {
	options := publicationFixture(t)
	installed, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(installed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json")); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(options.DestinationRoot, options.Version)
	if err == nil || result.Status == "removed" {
		t.Fatal("missing receipt without original intent authorized deletion")
	}
	after, err := os.ReadFile(installed.ManifestPath)
	if err != nil || !bytes.Equal(body, after) {
		t.Fatal("uncommitted uninstall changed original tree", err)
	}
	if fileExists(filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json")) {
		t.Fatal("missing receipt created deletion authority")
	}
}

func TestInstallReviewFixOwnedReaderDistinguishesInitialAbsence(t *testing.T) {
	for _, kind := range []string{"missing", "regular", "symlink", "dangling-symlink", "hardlink", "mode", "directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, "record")
			if kind != "missing" && kind != "directory" {
				if err := os.WriteFile(name, []byte("owned"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "symlink", "dangling-symlink":
				target := filepath.Join(root, "target")
				if err := os.Rename(name, target); err != nil {
					t.Fatal(err)
				}
				if kind == "dangling-symlink" {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, filepath.Join(root, "other-link")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			}
			limit := int64(4096)
			if kind == "oversized" {
				limit = 4
			}
			body, err := readOwnedRegular(name, 0600, limit)
			if kind == "missing" {
				if !errors.Is(err, os.ErrNotExist) || body != nil {
					t.Fatal("initial Lstat absence lost its identity", err)
				}
			} else if kind == "regular" {
				if err != nil || string(body) != "owned" {
					t.Fatal("safe regular file rejected", err)
				}
			} else if err == nil || errors.Is(err, os.ErrNotExist) || body != nil {
				t.Fatalf("present unsafe record treated as absence: %v", err)
			}
		})
	}
}

func TestInstallReviewFixReceiptPresenceSelectsCompleteProvenance(t *testing.T) {
	legacy := `{"version":"1.0.0","manifest_sha256":"` + strings.Repeat("a", 64) + `"}`
	if _, err := decodeManifestReceipt([]byte(legacy), "1.0.0"); err != nil {
		t.Fatal("fully omitted provenance is valid legacy", err)
	}
	for _, fields := range []string{
		`"request_sha256":""`,
		`"install_intent_sha256":""`,
		`"version_directory_identity":{}`,
		`"manifest_file_identity":{}`,
		`"request_sha256":"","install_intent_sha256":""`,
		`"request_sha256":null`,
		`"install_intent_sha256":null`,
		`"version_directory_identity":null`,
		`"manifest_file_identity":null`,
	} {
		t.Run(fields, func(t *testing.T) {
			body := strings.TrimSuffix(legacy, "}") + "," + fields + "}"
			if _, err := decodeManifestReceipt([]byte(body), "1.0.0"); err == nil {
				t.Fatal("explicit incomplete provenance admitted as legacy")
			}
		})
	}
}

func TestInstallReviewFixEmptyProvenanceCannotHideReplacement(t *testing.T) {
	for _, replaced := range []string{"root", "manifest"} {
		for _, operation := range []string{"pin", "doctor", "uninstall"} {
			t.Run(replaced+"/"+operation, func(t *testing.T) {
				options := publicationFixture(t)
				installed, err := InstallPackage(options)
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(installed.ManifestPath)
				if err != nil {
					t.Fatal(err)
				}
				if replaced == "manifest" {
					if err = os.Rename(installed.ManifestPath, filepath.Join(t.TempDir(), "original-manifest")); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(installed.ManifestPath, body, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					original := filepath.Join(t.TempDir(), "original-root")
					if err = os.Rename(installed.VersionRoot, original); err != nil {
						t.Fatal(err)
					}
					if err = os.Mkdir(installed.VersionRoot, 0700); err != nil {
						t.Fatal(err)
					}
					entries, err := os.ReadDir(original)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if err = os.Rename(filepath.Join(original, entry.Name()), filepath.Join(installed.VersionRoot, entry.Name())); err != nil {
							t.Fatal(err)
						}
					}
				}
				receiptPath := filepath.Join(options.DestinationRoot, "manifests", options.Version+".json")
				receipt, _ := json.Marshal(map[string]any{"version": options.Version, "manifest_sha256": installed.ManifestSHA256, "request_sha256": ""})
				if err = os.WriteFile(receiptPath, receipt, 0600); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "pin":
					if err = PinVersion(options.DestinationRoot, "review-task", options.Version); err == nil {
						t.Error("partial provenance pinned a replaced tree")
					}
				case "doctor":
					report, err := Doctor(options.DestinationRoot)
					if err == nil && report.Healthy {
						t.Error("partial provenance hid replacement from Doctor")
					}
				case "uninstall":
					result, err := UninstallPackage(options.DestinationRoot, options.Version)
					if err == nil || result.Status == "removed" || fileExists(filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json")) {
						t.Error("partial provenance created uninstall authority")
					}
				}
				after, err := os.ReadFile(installed.ManifestPath)
				index, e2 := os.ReadFile(receiptPath)
				if err != nil || e2 != nil || !bytes.Equal(after, body) || !bytes.Equal(index, receipt) {
					t.Fatal("refusal changed retained tree/index", err, e2)
				}
			})
		}
	}
}

func TestInstallReviewFixPendingGuardAnchorsParent(t *testing.T) {
	for _, kind := range []string{"symlink", "unsafe-mode"} {
		for _, operation := range []string{"pin", "runtime-discovery"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				options := publicationFixture(t)
				publicationInterrupt(t, options, "before-intent-cleanup")
				parent := filepath.Join(options.DestinationRoot, "install-intents")
				retained := filepath.Join(t.TempDir(), "original-intents")
				if err := os.Rename(parent, retained); err != nil {
					t.Fatal(err)
				}
				intentPath := filepath.Join(retained, options.Version+".json")
				identity, err := os.Lstat(intentPath)
				body, e2 := os.ReadFile(intentPath)
				if err != nil || e2 != nil {
					t.Fatal(err, e2)
				}
				if kind == "symlink" {
					if err = os.Symlink(t.TempDir(), parent); err != nil {
						t.Fatal(err)
					}
				} else if err = os.Mkdir(parent, 0755); err != nil {
					t.Fatal(err)
				}
				if operation == "pin" {
					err = PinVersion(options.DestinationRoot, "review-task", options.Version)
				} else {
					err = verifyInstalledVersion(options.DestinationRoot, options.Version)
				}
				if err == nil {
					t.Error("unsafe pending parent admitted committed content")
				}
				current, e1 := os.Lstat(intentPath)
				after, e2 := os.ReadFile(intentPath)
				if e1 != nil || e2 != nil || !os.SameFile(identity, current) || !bytes.Equal(body, after) {
					t.Fatal("unsafe-parent rejection changed original intent", e1, e2)
				}
			})
		}
	}
}

func TestInstallReviewFixPendingGuardKeepsCommittedAndLegacyParents(t *testing.T) {
	for _, kind := range []string{"committed", "legacy-missing-parent"} {
		t.Run(kind, func(t *testing.T) {
			options := publicationFixture(t)
			installed, err := InstallPackage(options)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "legacy-missing-parent" {
				receipt, _ := json.Marshal(manifestReceipt{Version: options.Version, ManifestSHA256: installed.ManifestSHA256})
				if err = os.WriteFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"), receipt, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(options.DestinationRoot, "install-intents")); err != nil {
					t.Fatal(err)
				}
			}
			if err = PinVersion(options.DestinationRoot, "review-task", options.Version); err != nil {
				t.Fatal("safe committed or legacy parent rejected", err)
			}
			if err = verifyInstalledVersion(options.DestinationRoot, options.Version); err != nil {
				t.Fatal("safe runtime content rejected", err)
			}
		})
	}
}

func TestInstallReviewFixNewReceiptRequiresPendingParent(t *testing.T) {
	for _, operation := range []string{"pin", "runtime-discovery"} {
		t.Run(operation, func(t *testing.T) {
			options := publicationFixture(t)
			installed, err := InstallPackage(options)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(installed.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(options.DestinationRoot, "install-intents")); err != nil {
				t.Fatal(err)
			}
			if operation == "pin" {
				err = PinVersion(options.DestinationRoot, "review-task", options.Version)
			} else {
				err = verifyInstalledVersion(options.DestinationRoot, options.Version)
			}
			if !errors.Is(err, ErrInstallPublicationConflict) {
				t.Error("new provenance with absent parent admitted", err)
			}
			after, err := os.ReadFile(installed.ManifestPath)
			if err != nil || !bytes.Equal(body, after) {
				t.Fatal("absent-parent refusal changed original version", err)
			}
		})
	}
}

func TestInstallReviewFixLegacyPayloadNeedsInitialAbsence(t *testing.T) {
	for _, kind := range []string{"missing", "unsafe-present", "dangling-symlink", "hook-without-manifest"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source, _ := makePackage(t, root, "1.0.0")
			name := filepath.Join(source, payloadManifestName)
			switch kind {
			case "unsafe-present":
				if err := os.WriteFile(name, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(root, "absent"), name); err != nil {
					t.Fatal(err)
				}
			case "hook-without-manifest":
				if err := os.MkdirAll(filepath.Join(source, "scripts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "scripts", "agent-bird-hook"), []byte("synthetic hook"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := validatePayload(source, false)
			if kind == "missing" && err != nil || kind != "missing" && err == nil {
				t.Fatalf("legacy eligibility changed: %v", err)
			}
		})
	}
}

func TestInstallReviewFixDoctorClassifiesPendingAndMissingParent(t *testing.T) {
	for _, kind := range []string{"new-missing", "legacy-missing", "pending"} {
		t.Run(kind, func(t *testing.T) {
			options := publicationFixture(t)
			if kind == "pending" {
				publicationInterrupt(t, options, "before-intent-cleanup")
			} else {
				installed, err := InstallPackage(options)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "legacy-missing" {
					body, _ := json.Marshal(manifestReceipt{Version: options.Version, ManifestSHA256: installed.ManifestSHA256})
					if err = os.WriteFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"), body, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err = os.Remove(filepath.Join(options.DestinationRoot, "install-intents")); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{filepath.Join(options.DestinationRoot, "versions", options.Version, "manifest.json"), filepath.Join(options.DestinationRoot, "manifests", options.Version+".json")}
			if kind == "pending" {
				paths = append(paths, filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json"))
			}
			before, identities := map[string][]byte{}, map[string]os.FileInfo{}
			for _, path := range paths {
				body, err := os.ReadFile(path)
				info, e2 := os.Lstat(path)
				if err != nil || e2 != nil {
					t.Fatal(err, e2)
				}
				before[path], identities[path] = body, info
			}
			report, err := Doctor(options.DestinationRoot)
			if err != nil {
				t.Fatal(err)
			}
			conflict, pending, identity := false, false, false
			for _, issue := range report.Issues {
				conflict = conflict || issue == "install_conflict:"+options.Version
				pending = pending || strings.HasPrefix(issue, "install_pending:"+options.Version+":")
				identity = identity || issue == options.Version+":publication_identity"
			}
			if kind == "new-missing" && (report.Healthy || !conflict) || kind == "legacy-missing" && !report.Healthy || kind == "pending" && (report.Healthy || !pending || conflict || identity) {
				t.Errorf("publication health category mismatch: %#v", report)
			}
			for _, path := range paths {
				after, err := os.ReadFile(path)
				current, e2 := os.Lstat(path)
				if err != nil || e2 != nil || !bytes.Equal(before[path], after) || !os.SameFile(identities[path], current) || identities[path].Mode() != current.Mode() {
					t.Fatal("Doctor changed original publication evidence", err, e2)
				}
			}
		})
	}
}
