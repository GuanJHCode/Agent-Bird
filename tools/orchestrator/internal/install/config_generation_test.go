package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ownedConfigPackage(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	_, err = InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	return dest, filepath.Join(dest, "versions/1.0.0/bin/codex-orchestrator")
}

func ownedConfigPublish(t *testing.T, executable, scope, id string, expected *ConfigGenerationActive, model string) ConfigGenerationReceipt {
	t.Helper()
	d := ConfigSavedDefaults{AGY: model, Claude: "claude-" + model, Codex: ""}
	r, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: id, Action: "publish", Expected: expected, SavedDefaults: &d})
	if err != nil || r.Active == nil || r.Ref == nil {
		t.Fatalf("publish %s: %#v %v", id, r, err)
	}
	return r
}

func TestOwnedConfigPublicationUsesOriginalRequest(t *testing.T) {
	_, executable := ownedConfigPackage(t)
	scope, err := contract.ConfigGenerationOwnerScope("original-controller", "birth-1")
	if err != nil {
		t.Fatal(err)
	}
	a := ownedConfigPublish(t, executable, scope, "publish-a", nil, "a")
	m, err := ReadConfigGeneration(executable, *a.Ref)
	if err != nil || m.SavedDefaults.AGY != "a" || m.SavedDefaults.Claude != "claude-a" || m.SavedDefaults.Codex != "" {
		t.Fatalf("manifest not exact: %#v %v", m, err)
	}
	b := ownedConfigPublish(t, executable, scope, "publish-b", a.Active, "b")
	replay := ownedConfigPublish(t, executable, scope, "publish-a", nil, "a")
	if !reflect.DeepEqual(replay, a) || b.Active.Revision != a.Active.Revision+1 {
		t.Fatalf("lost original receipt/monotonic revision: %#v %#v", replay, b)
	}
	drift := ConfigSavedDefaults{AGY: "different", Claude: "claude-a", Codex: ""}
	if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "publish-a", Action: "publish", SavedDefaults: &drift}); err == nil {
		t.Fatal("same request ID changed content accepted")
	}
	if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "no-expected", Action: "publish", SavedDefaults: &drift}); err == nil {
		t.Fatal("existing control mistaken for first absence")
	}
}

func TestOwnedConfigPreparationHoldSurvivesActiveSwitch(t *testing.T) {
	dest, executable := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	a := ownedConfigPublish(t, executable, scope, "a", nil, "a")
	use, err := ownedConfigBegin(t, executable, scope, "run-a", []string{"task-b", "task-a"}, strings.Repeat("d", 64))
	if err != nil || use.Ref == nil || *use.Ref != *a.Ref {
		t.Fatalf("selection lacks original durable hold: %#v %v", use, err)
	}
	b := ownedConfigPublish(t, executable, scope, "b", a.Active, "b")
	inactive, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "deactivate-b", Action: "deactivate", Expected: b.Active})
	if err != nil || inactive.Active == nil || inactive.Active.State != "inactive" || inactive.Active.Ref != nil {
		t.Fatalf("deactivate: %#v %v", inactive, err)
	}
	for _, phase := range []string{"before-private-checkpoint", "during-partial-pins"} {
		if phase == "during-partial-pins" {
			interrupted := errors.New("original-pin-prefix")
			if err = pinConfigPreparation(executable, use, strings.Repeat("e", 64), func(point string) error {
				if point == "pin:task-a" {
					return interrupted
				}
				return nil
			}); !errors.Is(err, interrupted) {
				t.Fatalf("did not interrupt after real first durable pin: %v", err)
			}
		}
		if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "retire-" + phase, Action: "retire", Expected: inactive.Active, Ref: a.Ref}); err == nil {
			t.Fatal("retired original preparation dependency", phase)
		}
		if _, err = UninstallPackage(dest, "1.0.0"); err == nil {
			t.Fatal("uninstalled original preparation dependency", phase)
		}
		replay, err := ownedConfigBegin(t, executable, scope, "run-a", []string{"task-a", "task-b"}, strings.Repeat("d", 64))
		if err != nil || !reflect.DeepEqual(replay, use) {
			t.Fatalf("selection replay changed to inactive/B: %#v %v", replay, err)
		}
	}
	if err = PinConfigPreparation(executable, use, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err = UnpinVersion(dest, "task-a"); err == nil {
		t.Fatal("generic unpin invented terminal proof for v2 use")
	}
	if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "retire-pinned", Action: "retire", Expected: inactive.Active, Ref: a.Ref}); err == nil {
		t.Fatal("retired v2 pinned reference")
	}
}

func TestOwnedConfigInactiveSelectsLegacyButKeepsOriginalUse(t *testing.T) {
	_, executable := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	a := ownedConfigPublish(t, executable, scope, "a", nil, "a")
	old, err := ownedConfigBegin(t, executable, scope, "old", []string{"old-task"}, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	r, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: a.Active})
	if err != nil || r.Active.Revision != 2 {
		t.Fatal("inactive revision lost", r, err)
	}
	fresh, err := ownedConfigBegin(t, executable, scope, "fresh", []string{"fresh-task"}, strings.Repeat("2", 64))
	if err != nil || fresh.Mode != "legacy" || fresh.Ref != nil {
		t.Fatal("inactive must freeze new legacy preparation", fresh, err)
	}
	ownedConfigPublish(t, executable, scope, "b", r.Active, "b")
	for _, use := range []ConfigPreparationUse{old, fresh} {
		got, err := ownedConfigBegin(t, executable, scope, use.RunID, use.TaskIDs, use.RequestSHA256)
		if err != nil || !reflect.DeepEqual(got, use) {
			t.Fatal("original generation/legacy choice changed", got, err)
		}
	}
}

func TestOwnedConfigSubmissionAndTrustedRelease(t *testing.T) {
	dest, executable := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	a := ownedConfigPublish(t, executable, scope, "a", nil, "a")
	use, err := ownedConfigBegin(t, executable, scope, "run", []string{"task"}, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	planHash := strings.Repeat("2", 64)
	if err = PinConfigPreparation(executable, use, planHash); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"kind": "fake", "provider": "antigravity-cli", "profile": map[string]any{"model": "a"}, "config_generation": a.Ref, "config_model_source": "saved-default"})
	tasks := []ConfigGenerationTaskUse{{TaskID: "task", AdapterPayload: raw}}
	if err = ValidateConfigGenerationSubmission(executable, scope, "run", planHash, tasks); err != nil {
		t.Fatalf("original authenticated package/use/model rejected: %v", err)
	}
	for _, bad := range []struct {
		name, scope, run, hash string
		raw                    []byte
	}{
		{"scope", strings.Repeat("f", 64), "run", planHash, raw},
		{"run", scope, "other", planHash, raw},
		{"hash", scope, "run", strings.Repeat("3", 64), raw},
		{"default", scope, "run", planHash, []byte(strings.Replace(string(raw), `"model":"a"`, `"model":"other"`, 1))},
		{"fallback-legacy", scope, "run", planHash, raw},
	} {
		t.Run(bad.name, func(t *testing.T) {
			items := []ConfigGenerationTaskUse{{TaskID: "task", AdapterPayload: bad.raw}}
			if bad.name == "fallback-legacy" {
				items[0].Fallbacks = []json.RawMessage{json.RawMessage(`{"kind":"fake"}`)}
			}
			if err := ValidateConfigGenerationSubmission(executable, bad.scope, bad.run, bad.hash, items); err == nil {
				t.Fatal("invalid submission accepted")
			}
		})
	}
	if err = ReleaseConfigTaskUse(executable, "other", "task", a.Ref); err == nil {
		t.Fatal("foreign run erased original use")
	}
	if err = ReleaseConfigTaskUse(executable, "run", "task", nil); err == nil {
		t.Fatal("legacy release erased version2 use")
	}
	if err = ReleaseConfigTaskUse(executable, "run", "task", a.Ref); !errors.Is(err, ErrConfigPreparationStoreMismatch) {
		t.Fatal("naked release acquired Store authority", err)
	}
	if err = ReleaseConfigTaskUseWithStore(executable, "run", "task", a.Ref, func(got ConfigPreparationUse, remove func() error) error {
		if !reflect.DeepEqual(got, use) {
			return ErrConfigGenerationConflict
		}
		return remove()
	}); err != nil {
		t.Fatalf("trusted original terminal use not released: %v", err)
	}
	inactive, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: a.Active})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "retire", Action: "retire", Expected: inactive.Active, Ref: a.Ref}); err != nil {
		t.Fatalf("retire after exact trusted release: %v", err)
	}
	if result, err := UninstallPackage(dest, "1.0.0"); err != nil || result.Status != "removed" {
		t.Fatalf("inactive tombstone/history incorrectly pins released package: %#v %v", result, err)
	}
}

func TestOwnedConfigMalformedStateBlocksPackageUninstall(t *testing.T) {
	for _, state := range []string{"symlink", "world-readable", "malformed-owner"} {
		t.Run(state, func(t *testing.T) {
			dest, _ := ownedConfigPackage(t)
			config := filepath.Join(dest, "owned-config")
			switch state {
			case "symlink":
				foreign := filepath.Join(filepath.Dir(dest), "foreign")
				if err := os.Mkdir(foreign, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, config); err != nil {
					t.Fatal(err)
				}
			case "world-readable":
				if err := os.Mkdir(config, 0755); err != nil {
					t.Fatal(err)
				}
			case "malformed-owner":
				if err := os.Mkdir(config, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(config, "foreign-owner"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if result, err := UninstallPackage(dest, "1.0.0"); err == nil {
				t.Fatalf("malformed owned-generation state must retain package: %#v", result)
			}
			if _, err := os.Lstat(filepath.Join(dest, "versions/1.0.0")); err != nil {
				t.Fatalf("uninstall lost original package: %v", err)
			}
		})
	}
}

func ownedConfigTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			name, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			out[name] = string(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOwnedConfigCompletedHistoryDoesNotExhaustLiveCapacity(t *testing.T) {
	dest, executable := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	var active *ConfigGenerationActive
	var first ConfigGenerationReceipt
	for i := 0; i < 64; i++ {
		r := ownedConfigPublish(t, executable, scope, fmt.Sprintf("publish-%d", i), active, "same")
		if i == 0 {
			first = r
		}
		active = r.Active
	}
	defaults := ConfigSavedDefaults{AGY: "same", Claude: "claude-same", Codex: ""}
	receipt, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "publish-64", Action: "publish", Expected: active, SavedDefaults: &defaults})
	if err != nil {
		t.Errorf("one live generation must not exhaust publication history: %v", err)
		t.Logf("original owned files retained: %v", reflect.ValueOf(ownedConfigTree(t, filepath.Join(dest, "owned-config", scope))).MapKeys())
	} else {
		active = receipt.Active
	}
	if _, err := ReadConfigGenerationActive(executable, scope); err != nil {
		t.Errorf("status poisoned: %v", err)
	}
	replay := ownedConfigPublish(t, executable, scope, "publish-0", nil, "same")
	if !reflect.DeepEqual(replay, first) {
		t.Fatal("history beyond 64 lost original receipt")
	}
	current, err := ReadConfigGenerationActive(executable, scope)
	if err != nil || !reflect.DeepEqual(current, active) {
		t.Fatal("historical replay changed current active", err)
	}

	if _, err := ownedConfigBegin(t, executable, scope, "run", []string{"task"}, strings.Repeat("1", 64)); err != nil {
		t.Errorf("preparation poisoned: %v", err)
	}
	if _, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: active}); err != nil {
		t.Errorf("deactivate poisoned by completed history: %v", err)
	}
}

func TestOwnedConfigFullLiveCapacityRejectsWithoutSideEffects(t *testing.T) {
	dest, executable := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	var active *ConfigGenerationActive
	for i := 0; i < 64; i++ {
		active = ownedConfigPublish(t, executable, scope, fmt.Sprintf("publish-%d", i), active, fmt.Sprintf("model-%d", i)).Active
	}
	root := filepath.Join(dest, "owned-config", scope)
	before := ownedConfigTree(t, root)
	defaults := ConfigSavedDefaults{AGY: "model-64", Claude: "claude-model-64", Codex: ""}
	if _, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "overfull", Action: "publish", Expected: active, SavedDefaults: &defaults}); err == nil {
		t.Error("overfull live generation accepted")
	}
	if after := ownedConfigTree(t, root); !reflect.DeepEqual(before, after) {
		t.Error("capacity rejection created a new stage or intent")
	}
	off, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: active})
	if err != nil {
		t.Fatalf("capacity blocked final deactivate: %v", err)
	}
	if _, err = UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "retire", Action: "retire", Expected: off.Active, Ref: active.Ref}); err != nil {
		t.Fatalf("capacity blocked retirement: %v", err)
	}
	ownedConfigPublish(t, executable, scope, "after-retirement", off.Active, "new-live-slot")

}

func TestOwnedConfigConflictingPendingHoldBlocksConsumers(t *testing.T) {
	for _, pending := range []string{"malformed", "different"} {
		t.Run(pending, func(t *testing.T) {
			dest, executable := ownedConfigPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			a := ownedConfigPublish(t, executable, scope, "a", nil, "a")
			use, err := ownedConfigBegin(t, executable, scope, "run", []string{"task"}, strings.Repeat("1", 64))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dest, "owned-config", scope, "preparations", publicationBytesDigest([]byte("run"))+".json")
			b := ownedConfigPublish(t, executable, scope, "b", a.Active, "b")
			c := ownedConfigPublish(t, executable, scope, "c", b.Active, "c")
			raw := []byte("{")
			if pending == "different" {
				use.RequestSHA256 = strings.Repeat("2", 64)
				use.Ref = b.Ref
				raw, _ = json.Marshal(use)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".pending-"+filepath.Base(path)), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ownedConfigBegin(t, executable, scope, "run", []string{"task"}, strings.Repeat("1", 64)); err == nil {
				t.Error("final hold silently hid conflicting pending record")
			}
			if _, err := UpdateConfigGeneration(executable, scope, ConfigGenerationOperation{RequestID: "retire-b", Action: "retire", Expected: c.Active, Ref: b.Ref}); err == nil {
				t.Error("retirement ignored ambiguous preparation")
			}
			if _, err := os.Lstat(filepath.Join(dest, "owned-config", scope, "generations", b.Ref.GenerationID+".json")); err != nil {
				t.Fatal("pending B dependency was deleted", err)
			}
			if _, err := UninstallPackage(dest, "1.0.0"); err == nil {
				t.Error("uninstall ignored ambiguous preparation")
			}
		})
	}
}

func TestOwnedConfigOriginalPublicationRecoversEachDurablePhase(t *testing.T) {
	for _, point := range []string{"intent", "manifest", "active", "receipt"} {
		t.Run(point, func(t *testing.T) {
			_, exe := ownedConfigPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			a := ownedConfigPublish(t, exe, scope, "a", nil, "a")
			d := ConfigSavedDefaults{AGY: "b", Claude: "claude-b", Codex: ""}
			request := ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: a.Active, SavedDefaults: &d}
			interrupted := errors.New("durable-phase-interrupted")
			if _, err := updateConfigGeneration(exe, scope, request, func(p string) error {
				if p == point {
					return interrupted
				}
				return nil
			}); !errors.Is(err, interrupted) {
				t.Fatal("missing actual interruption", err)
			}
			if _, err := ReadConfigGenerationActive(exe, scope); !errors.Is(err, ErrConfigGenerationPending) {
				t.Fatal("pending publication not fenced", err)
			}
			// A completed old request remains replayable without completing B.
			old := ownedConfigPublish(t, exe, scope, "a", nil, "a")
			if !reflect.DeepEqual(old, a) {
				t.Fatal("old receipt changed")
			}
			if _, err := ReadConfigGenerationActive(exe, scope); !errors.Is(err, ErrConfigGenerationPending) {
				t.Fatal("old receipt cleared foreign pending", err)
			}
			b, err := UpdateConfigGeneration(exe, scope, request)
			if err != nil || b.Active.Revision != 2 {
				t.Fatal("original publication did not recover", b, err)
			}
			current, err := ReadConfigGenerationActive(exe, scope)
			if err != nil || !reflect.DeepEqual(current, b.Active) {
				t.Fatal("recovered current mismatched", err)
			}
		})
	}
}

func TestOwnedConfigReceiptPathMustRemainAnchoredBeforeCleanup(t *testing.T) {
	dest, exe := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	defaults := ConfigSavedDefaults{AGY: "a", Claude: "b", Codex: ""}
	request := ConfigGenerationOperation{RequestID: "original", Action: "publish", SavedDefaults: &defaults}
	key := publicationBytesDigest([]byte(request.RequestID))
	owner := filepath.Join(dest, "owned-config", scope)
	shard := filepath.Join(owner, "requests", key[:2])
	backup := shard + "-original-backup"
	_, err := updateConfigGeneration(exe, scope, request, func(point string) error {
		if point != "receipt" {
			return nil
		}
		if e := os.Rename(shard, backup); e != nil {
			return e
		}
		return os.Mkdir(shard, 0700)
	})
	if err == nil {
		t.Error("returned success after original receipt directory was replaced")
	}
	if _, e := os.Lstat(filepath.Join(owner, "pending.json")); e != nil {
		t.Error("unknown receipt path discarded original pending intent", e)
	}
	if _, e := os.Lstat(filepath.Join(backup, key+".json")); e != nil {
		t.Fatal("original receipt was not preserved", e)
	}
	if e := os.Remove(shard); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(backup, shard); e != nil {
		t.Fatal(e)
	}
	receipt, err := UpdateConfigGeneration(exe, scope, request)
	if err != nil || receipt.Active == nil || receipt.Active.Revision != 1 {
		t.Fatal("restored exact original request failed replay", receipt, err)
	}
}

func TestOwnedConfigPinAnchorMustPrecedePreparationHoldCleanup(t *testing.T) {
	for _, changed := range []string{"directory", "record"} {
		t.Run(changed, func(t *testing.T) {
			dest, exe := ownedConfigPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			ownedConfigPublish(t, exe, scope, "a", nil, "a")
			use, err := ownedConfigBegin(t, exe, scope, "run", []string{"task"}, strings.Repeat("1", 64))
			if err != nil {
				t.Fatal(err)
			}
			pins := filepath.Join(dest, "pins")
			pinPath := filepath.Join(pins, "task.json")
			backup := pins + "-original-backup"
			if changed == "record" {
				backup = pinPath + ".original-backup"
			}
			err = pinConfigPreparation(exe, use, strings.Repeat("2", 64), func(point string) error {
				if point != "pin:task" {
					return nil
				}
				if changed == "directory" {
					if e := os.Rename(pins, backup); e != nil {
						return e
					}
					return os.Mkdir(pins, 0700)
				}
				if e := os.Rename(pinPath, backup); e != nil {
					return e
				}
				original, e := os.ReadFile(backup)
				if e != nil {
					return e
				}
				return os.WriteFile(pinPath, original, 0600)
			})
			if err == nil {
				t.Error("returned success after original pin identity was replaced")
			}
			hold := filepath.Join(dest, "owned-config", scope, "preparations", publicationBytesDigest([]byte("run"))+".json")
			if _, e := os.Lstat(hold); e != nil {
				t.Error("unknown pin path discarded original preparation hold", e)
			}
			if changed == "directory" {
				if e := os.Remove(pins); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(backup, pins); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.Remove(pinPath); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(backup, pinPath); e != nil {
					t.Fatal(e)
				}
			}
			if err = PinConfigPreparation(exe, use, strings.Repeat("2", 64)); err != nil {
				t.Fatal("restored original pin failed exact conversion", err)
			}
		})
	}
}

func TestOwnedConfigLegacyPackageCanKeepLegacyPreparation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, binary := makePackage(t, root, "1.0.0")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "installed"), DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator")
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	use, err := ownedConfigBegin(t, exe, scope, "run", []string{"task"}, strings.Repeat("1", 64))
	if err != nil || use.Mode != "legacy" || use.Ref != nil {
		t.Fatalf("original validated legacy installation lost no-active branch: %#v %v", use, err)
	}
	if err = PinConfigPreparation(exe, use, strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	defaults := ConfigSavedDefaults{AGY: "a", Claude: "b", Codex: ""}
	if _, err = UpdateConfigGeneration(exe, scope, ConfigGenerationOperation{RequestID: "publish", Action: "publish", SavedDefaults: &defaults}); err == nil {
		t.Fatal("legacy package gained unverified generation payload authority")
	}
}

func TestOwnedConfigDoctorReportsPendingWithoutRecoveringIt(t *testing.T) {
	dest, exe := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	a := ownedConfigPublish(t, exe, scope, "a", nil, "a")
	values := ConfigSavedDefaults{AGY: "b", Claude: "claude-b", Codex: ""}
	interrupted := errors.New("pending-publication")
	if _, err := updateConfigGeneration(exe, scope, ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: a.Active, SavedDefaults: &values}, func(point string) error {
		if point == "intent" {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	before := ownedConfigTree(t, filepath.Join(dest, "owned-config"))
	report, err := Doctor(dest)
	if err != nil || report.Healthy {
		t.Errorf("Doctor misreported pending owned publication: %#v %v", report, err)
	}
	if after := ownedConfigTree(t, filepath.Join(dest, "owned-config")); !reflect.DeepEqual(before, after) {
		t.Error("Doctor recovered/mutated original owned state")
	}
}

func TestOwnedConfigMalformedV2PinBlocksLegacyUninstall(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, binary := makePackage(t, root, "1.0.0")
	dest := filepath.Join(root, "installed")
	if _, err = InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	ref := contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
	invalid := pin{SchemaVersion: 2, RunID: "run", TaskID: "task", Version: "foreign", ConfigGeneration: &ref, PreparationSHA256: strings.Repeat("d", 64)}
	raw, _ := json.Marshal(invalid)
	if err = os.WriteFile(filepath.Join(dest, "pins", "task.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = UninstallPackage(dest, "1.0.0"); err == nil {
		t.Error("legacy pin checker ignored malformed new ref/version binding")
	}
	if _, err = os.Lstat(filepath.Join(dest, "versions", "1.0.0")); err != nil {
		t.Error("malformed use lost original package", err)
	}
}

// FS-only tests do not claim owner grant authentication; consumer tests use DB.
func ownedConfigBegin(t *testing.T, exe, scope, run string, tasks []string, request string) (ConfigPreparationUse, error) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe))))
	state := filepath.Join(root, "authority-state")
	if e := os.MkdirAll(state, 0700); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(state, "state.db")
	if _, e := os.Lstat(db); errors.Is(e, os.ErrNotExist) {
		if e = os.WriteFile(db, []byte("synthetic node identity only"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	a := configFileAuthority(t, state, db, "fs-only-grant")
	return BeginConfigPreparation(exe, scope, run, tasks, request, &a)
}
func configFileAuthority(t *testing.T, state, db, grant string) contract.ConfigPreparationAuthority {
	t.Helper()
	r, e := os.Lstat(state)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Lstat(db)
	if e != nil {
		t.Fatal(e)
	}
	rs := r.Sys().(*syscall.Stat_t)
	fs := f.Sys().(*syscall.Stat_t)
	return contract.ConfigPreparationAuthority{SchemaVersion: 1, OwnerGrantID: grant, StateRoot: state, StateDevice: uint64(rs.Dev), StateInode: rs.Ino, DatabaseDevice: uint64(fs.Dev), DatabaseInode: fs.Ino}
}

func TestOwnedConfigOriginalUseHistoryIsBoundedAndAnchored(t *testing.T) {
	for _, scenario := range []string{"full-conversion", "malformed-pending", "shard-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			dest, exe := ownedConfigPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			ownedConfigPublish(t, exe, scope, "a", nil, "a")
			use, e := ownedConfigBegin(t, exe, scope, "run", []string{"one", "two"}, strings.Repeat("1", 64))
			if e != nil {
				t.Fatal(e)
			}
			key := publicationBytesDigest([]byte("run"))
			dir := filepath.Join(dest, "owned-config", scope, "uses", key[:2])
			path := filepath.Join(dir, key+".json")
			if scenario == "shard-replacement" {
				backup := dir + "-original"
				e = pinConfigPreparation(exe, use, strings.Repeat("2", 64), func(point string) error {
					if point != "pin:one" {
						return nil
					}
					if e := os.Rename(dir, backup); e != nil {
						return e
					}
					return os.Mkdir(dir, 0700)
				})
				if e == nil {
					t.Fatal("lost original use history cleared preparation hold")
				}
				hold := filepath.Join(dest, "owned-config", scope, "preparations", key+".json")
				if _, e = os.Lstat(hold); e != nil {
					t.Fatal("unproved original use lost hold", e)
				}
				if e = os.Remove(dir); e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(backup, dir); e != nil {
					t.Fatal(e)
				}
			}
			if e = PinConfigPreparation(exe, use, strings.Repeat("2", 64)); e != nil {
				t.Fatal(e)
			}
			if _, e = os.Lstat(filepath.Join(dest, "owned-config", scope, "preparations", key+".json")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("full conversion still holds live preparation", e)
			}
			if scenario == "malformed-pending" {
				if e = os.WriteFile(filepath.Join(dir, ".pending-"+key+".json"), []byte("{"), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = ReadConfigPreparationAuthority(exe, scope, "run"); !errors.Is(e, ErrConfigGenerationPending) {
					t.Fatal("original final hid malformed pending", e)
				}
				return
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			var original ConfigPreparationUse
			if e = generationDecode(raw, &original, generationRecordLimit); e != nil || !reflect.DeepEqual(original, use) {
				t.Fatal("completed original use lost full tuple", e)
			}
			altered := use
			altered.TaskIDs = []string{"one"}
			if e = PinConfigPreparation(exe, altered, strings.Repeat("2", 64)); e == nil {
				t.Fatal("task subset fabricated original use")
			}
			if _, e = BeginConfigPreparation(exe, scope, "run", use.TaskIDs, use.RequestSHA256, use.Authority); e == nil || e.Error() != "config_original_preparation_required" {
				t.Fatal("missing CPP recreated full conversion", e)
			}
		})
	}
}
