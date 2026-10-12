package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallReviewFixReceiptRequiresExactWireKeys(t *testing.T) {
	options := publicationFixture(t)
	_, err := InstallPackage(options)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var complete map[string]json.RawMessage
	if err = json.Unmarshal(body, &complete); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]json.RawMessage{"version": complete["version"], "manifest_sha256": complete["manifest_sha256"]}
	for name, value := range map[string]any{"canonical-legacy": legacy, "canonical-new": complete} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(value)
			if _, err := decodeManifestReceipt(body, options.Version); err != nil {
				t.Fatal("declared canonical wire form rejected", err)
			}
		})
	}
	for _, test := range []struct{ canonical, upper, mixed, empty string }{
		{"version", "VERSION", "Version", ""},
		{"manifest_sha256", "MANIFEST_SHA256", "Manifest_SHA256", ""},
		{"request_sha256", "REQUEST_SHA256", "Request_SHA256", `""`},
		{"install_intent_sha256", "INSTALL_INTENT_SHA256", "Install_Intent_SHA256", `""`},
		{"version_directory_identity", "VERSION_DIRECTORY_IDENTITY", "Version_Directory_Identity", `{}`},
		{"manifest_file_identity", "MANIFEST_FILE_IDENTITY", "Manifest_File_Identity", `{}`},
	} {
		values := map[string]json.RawMessage{"nonempty": complete[test.canonical]}
		if test.empty != "" {
			values["empty"] = json.RawMessage(test.empty)
		}
		for _, alias := range []string{test.upper, test.mixed} {
			for label, value := range values {
				t.Run(alias+"/"+label, func(t *testing.T) {
					request := map[string]json.RawMessage{"version": legacy["version"], "manifest_sha256": legacy["manifest_sha256"]}
					delete(request, test.canonical)
					request[alias] = value
					body, _ := json.Marshal(request)
					if _, err := decodeManifestReceipt(body, options.Version); err == nil {
						t.Fatal("case-folded alias admitted outside exact receipt wire fields")
					}
				})
			}
		}
	}
}

func TestInstallReviewFixReceiptAliasesCannotBypassLifecycle(t *testing.T) {
	for _, alias := range []string{"REQUEST_SHA256", "Request_SHA256"} {
		for _, value := range []string{"", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
			label := "empty"
			if value != "" {
				label = "nonempty"
			}
			for _, operation := range []string{"pin", "doctor", "uninstall"} {
				t.Run(alias+"/"+label+"/"+operation, func(t *testing.T) {
					options := publicationFixture(t)
					installed, err := InstallPackage(options)
					if err != nil {
						t.Fatal(err)
					}
					body, err := os.ReadFile(installed.ManifestPath)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.Rename(installed.ManifestPath, filepath.Join(t.TempDir(), "original-manifest")); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(installed.ManifestPath, body, 0600); err != nil {
						t.Fatal(err)
					}
					receiptPath := filepath.Join(options.DestinationRoot, "manifests", options.Version+".json")
					receipt, _ := json.Marshal(map[string]any{"version": options.Version, "manifest_sha256": installed.ManifestSHA256, alias: value})
					if err = os.WriteFile(receiptPath, receipt, 0600); err != nil {
						t.Fatal(err)
					}
					func() {
						// An invalid record must be rejected, never crash a real public
						// consumer. Recover only to retain this pure fixture's evidence.
						defer func() {
							if recover() != nil {
								t.Error("aliased receipt crashed lifecycle instead of refusal")
							}
						}()
						switch operation {
						case "pin":
							if err := PinVersion(options.DestinationRoot, "review-task", options.Version); err == nil {
								t.Error("aliased provenance pinned replaced manifest")
							}
						case "doctor":
							report, err := Doctor(options.DestinationRoot)
							if err == nil && report.Healthy {
								t.Error("aliased provenance hid replacement from Doctor")
							}
						case "uninstall":
							result, err := UninstallPackage(options.DestinationRoot, options.Version)
							if err == nil || result.Status == "removed" || fileExists(filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json")) {
								t.Error("aliased provenance created uninstall authority")
							}
						}
					}()
					after, err := os.ReadFile(installed.ManifestPath)
					index, e2 := os.ReadFile(receiptPath)
					if err != nil || e2 != nil || !bytes.Equal(body, after) || !bytes.Equal(receipt, index) {
						t.Fatal("rejected alias changed retained version/index", err, e2)
					}
				})
			}
		}
	}
}
