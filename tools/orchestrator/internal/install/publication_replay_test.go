package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallPublicationCheckpointReplay(t *testing.T) {
	points := []string{"stage-sealed", "intent-published", "version-published", "version-parent-synced", "index-published", "index-parent-synced", "before-intent-cleanup", "intent-cleanup-renamed", "intent-cleaned"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			options := publicationFixture(t)
			interrupted := errors.New("checkpoint-interrupted")
			hits := 0
			_, err := installPackage(options, func(actual string) error {
				if actual == point {
					hits++
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) || hits != 1 {
				t.Fatalf("checkpoint: %v %d", err, hits)
			}
			versions := filepath.Join(options.DestinationRoot, "versions")
			entries, err := os.ReadDir(versions)
			if err != nil || len(entries) != 1 {
				t.Fatalf("one original tree expected: %v %v", entries, err)
			}
			oldRoot := filepath.Join(versions, entries[0].Name())
			original, _ := os.Stat(oldRoot)
			manifestBytes, err := os.ReadFile(filepath.Join(oldRoot, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var originalReceipt []byte
			if point != "stage-sealed" {
				originalReceipt, _ = os.ReadFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"))
			}
			if point == "intent-cleanup-renamed" || point == "before-intent-cleanup" || point == "index-parent-synced" || point == "index-published" {
				if err = PinVersion(options.DestinationRoot, "pending-task", options.Version); !errors.Is(err, ErrInstallPublicationPending) {
					t.Fatalf("pin admitted pending commit: %v", err)
				}
			}
			first, err := InstallPackage(options)
			if err != nil {
				t.Fatal("exact replay", err)
			}
			second, err := InstallPackage(options)
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("idempotent result drift: %v %#v %#v", err, first, second)
			}
			after, _ := os.Stat(first.VersionRoot)
			actual, _ := os.ReadFile(first.ManifestPath)
			if point == "stage-sealed" {
				if os.SameFile(original, after) {
					t.Fatal("unbound pre-intent stage adopted")
				}
				retained, err := os.Stat(oldRoot)
				if err != nil || !os.SameFile(original, retained) {
					t.Fatal("unbound preparation deleted", err)
				}
			} else if !os.SameFile(original, after) || !bytes.Equal(manifestBytes, actual) {
				t.Fatal("replay rebuilt original sealed tree")
			}
			if len(originalReceipt) != 0 {
				current, _ := os.ReadFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"))
				if !bytes.Equal(current, originalReceipt) {
					t.Fatal("original commit receipt replaced")
				}
			}
			intents, err := os.ReadDir(filepath.Join(options.DestinationRoot, "install-intents"))
			if err != nil || len(intents) != 0 {
				t.Fatalf("matching committed intent not cleaned: %v %v", intents, err)
			}
			if err = PinVersion(options.DestinationRoot, "committed-task", options.Version); err != nil {
				t.Fatal(err)
			}
			if err = UnpinVersion(options.DestinationRoot, "committed-task"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInstallPublicationChangedRequestRetainsEvidence(t *testing.T) {
	for _, change := range []string{"source-bytes", "source-mode", "source-directory-mode", "empty-directory", "binary-bytes", "binary-mode", "source-path", "binary-path", "data-path"} {
		t.Run(change, func(t *testing.T) {
			options := publicationFixture(t)
			publicationInterrupt(t, options, "version-published")
			intentPath := filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json")
			before, _ := os.ReadFile(intentPath)
			root := filepath.Join(options.DestinationRoot, "versions", options.Version)
			identity, _ := os.Stat(root)
			switch change {
			case "source-bytes":
				f, err := os.OpenFile(filepath.Join(options.SourceRoot, ".codex-plugin", "plugin.json"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.Write([]byte(" \n"))
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "source-mode":
				if err := os.Chmod(filepath.Join(options.SourceRoot, ".codex-plugin", "plugin.json"), 0400); err != nil {
					t.Fatal(err)
				}
			case "source-directory-mode":
				name := filepath.Join(options.SourceRoot, ".codex-plugin")
				defer os.Chmod(name, 0700)
				if err := os.Chmod(name, 0500); err != nil {
					t.Fatal(err)
				}
			case "empty-directory":
				if err := os.Mkdir(filepath.Join(options.SourceRoot, "empty"), 0700); err != nil {
					t.Fatal(err)
				}
			case "binary-bytes":
				if err := os.WriteFile(options.BinaryPath, []byte("different-binary"), 0700); err != nil {
					t.Fatal(err)
				}
			case "binary-mode":
				if err := os.Chmod(options.BinaryPath, 0500); err != nil {
					t.Fatal(err)
				}
			case "source-path":
				moved := options.SourceRoot + "-moved"
				if err := os.Rename(options.SourceRoot, moved); err != nil {
					t.Fatal(err)
				}
				options.SourceRoot = moved
			case "binary-path":
				moved := options.BinaryPath + "-moved"
				if err := os.Rename(options.BinaryPath, moved); err != nil {
					t.Fatal(err)
				}
				options.BinaryPath = moved
			case "data-path":
				options.DataRoot += "-other"
			}
			if _, err := InstallPackage(options); !errors.Is(err, ErrInstallPublicationConflict) {
				t.Fatalf("changed request admitted: %v", err)
			}
			after, _ := os.ReadFile(intentPath)
			current, _ := os.Stat(root)
			if !bytes.Equal(before, after) || !os.SameFile(identity, current) {
				t.Fatal("conflict modified retained original publication")
			}
		})
	}
}

func TestInstallPublicationUnavailableSourceDoesNotChangePending(t *testing.T) {
	for _, input := range []string{"source", "binary"} {
		t.Run(input, func(t *testing.T) {
			options := publicationFixture(t)
			publicationInterrupt(t, options, "version-published")
			intentPath := filepath.Join(options.DestinationRoot, "install-intents", options.Version+".json")
			before, _ := os.ReadFile(intentPath)
			name := options.SourceRoot
			if input == "binary" {
				name = options.BinaryPath
			}
			if err := os.Rename(name, name+"-retained"); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallPackage(options); !errors.Is(err, ErrInstallOriginalInputsRequired) {
				t.Fatalf("missing original input: %v", err)
			}
			after, _ := os.ReadFile(intentPath)
			if !bytes.Equal(before, after) {
				t.Fatal("missing-input recovery modified intent")
			}
			report, err := Doctor(options.DestinationRoot)
			if err != nil {
				t.Fatal(err)
			}
			if !containsIssue(report.Issues, "original_install_inputs_required:"+options.Version) {
				t.Fatalf("Doctor omitted original-input requirement: %#v", report)
			}
		})
	}
}

func containsIssue(issues []string, target string) bool {
	for _, issue := range issues {
		if issue == target {
			return true
		}
	}
	return false
}

func TestInstallPublicationLegacyReceiptCannotBeAdopted(t *testing.T) {
	options := publicationFixture(t)
	result, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(options.DestinationRoot, "manifests", options.Version+".json")
	body, _ := json.Marshal(manifestReceipt{Version: options.Version, ManifestSHA256: result.ManifestSHA256})
	if err = os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err = InstallPackage(options); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("legacy receipt adopted: %v", err)
	}
	if err = verifyInstalledVersion(options.DestinationRoot, options.Version); err != nil {
		t.Fatal("legacy read compatibility", err)
	}
	report, err := Doctor(options.DestinationRoot)
	if err != nil || !report.Healthy {
		t.Fatalf("legacy Doctor: %#v %v", report, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("legacy receipt rewritten")
	}
}

func TestInstallPublicationBoundsActualCopyAndDescriptors(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		root := t.TempDir()
		inputName, outputName := filepath.Join(root, "input"), filepath.Join(root, "output")
		if err := os.WriteFile(inputName, []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(inputName)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		output, err := os.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		n, err := publicationCopyBounded(output, input, 4)
		output.Close()
		if err == nil || n != 4 {
			t.Fatalf("over-budget input must be rejected: %d %v", n, err)
		}
		body, _ := os.ReadFile(outputName)
		if string(body) != "1234" {
			t.Fatalf("wrote more than bounded actual byte count: %q", body)
		}
		input.Seek(0, io.SeekStart)
		var exact bytes.Buffer
		if n, err = publicationCopyBounded(&exact, input, 5); err != nil || n != 5 || exact.String() != "12345" {
			t.Fatal(n, err)
		}
	})
	t.Run("descriptors", func(t *testing.T) {
		options := publicationFixture(t)
		p, err := prepareInstallRequest(options)
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		for _, change := range []string{"total", "binary", "files", "directories", "depth", "empty-dir-digest"} {
			t.Run(change, func(t *testing.T) {
				body, _ := json.Marshal(p.request)
				var request installRequest
				json.Unmarshal(body, &request)
				switch change {
				case "total":
					request.Payload[0].Size = installMaxBytes
				case "binary":
					request.Binary.Size = installMaxBinaryBytes + 1
				case "files":
					request.Payload = make([]installPayloadFile, installMaxFiles+1)
				case "directories":
					request.Directories = make([]installPayloadDirectory, installMaxFiles+1)
				case "depth":
					request.Payload[0].Path = strings.Repeat("a/", 66) + "file"
				case "empty-dir-digest":
					if err := os.Mkdir(filepath.Join(options.SourceRoot, "zz-empty"), 0700); err != nil {
						t.Fatal(err)
					}
					q, err := prepareInstallRequest(options)
					if err != nil {
						t.Fatal(err)
					}
					defer q.close()
					if p.digest == q.digest {
						t.Fatal("empty source directory excluded from logical request")
					}
					return
				}
				if _, err := installExpectedPayload(request); err == nil {
					t.Fatal("over-limit descriptor admitted")
				}
			})
		}
	})
}
