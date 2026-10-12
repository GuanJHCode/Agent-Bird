package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB07ConfigOwnedPlanApplyRemovePreservesOthers(t *testing.T) {
	for _, kind := range []string{"claude-settings", "agy-hooks"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "config")
			os.Mkdir(parent, 0700)
			target := filepath.Join(parent, "settings.json")
			before := []byte(`{"unrelated":{"private":"kept"},"hooks":{}}`)
			if kind == "agy-hooks" {
				before = []byte(`{"unrelated":{"PreInvocation":[{"type":"command","command":"keep"}]}}`)
			}
			os.WriteFile(target, before, 0600)
			plan := filepath.Join(parent, "plan.json")
			request := ConfigPlanRequest{TargetType: kind, Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}
			if _, err = PlanConfig(request); err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(target)
			if string(body) != string(before) {
				t.Fatal("plan mutated config")
			}
			if _, err = ApplyConfig(plan); err != nil {
				t.Fatal(err)
			}
			if _, err = ApplyConfig(plan); err != nil {
				t.Fatalf("repeat %v", err)
			}
			body, _ = os.ReadFile(target)
			var current map[string]any
			json.Unmarshal(body, &current)
			current["later"] = "preserved"
			body, _ = json.Marshal(current)
			os.WriteFile(target, body, 0600)
			request.PlanPath = filepath.Join(parent, "remove.json")
			request.Action = "remove"
			if _, err = PlanConfig(request); err != nil {
				t.Fatal(err)
			}
			if _, err = ApplyConfig(request.PlanPath); err != nil {
				t.Fatal(err)
			}
			if _, err = ApplyConfig(request.PlanPath); err != nil {
				t.Fatal(err)
			}
			body, _ = os.ReadFile(target)
			json.Unmarshal(body, &current)
			if current["later"] != "preserved" || current["unrelated"] == nil {
				t.Fatal("unrelated config lost")
			}
		})
	}
}
func TestB07ConfigConflictAndPrivateInputs(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "config")
	os.Mkdir(parent, 0700)
	target := filepath.Join(parent, "settings.json")
	os.WriteFile(target, []byte(`{}`), 0600)
	plan := filepath.Join(parent, "plan.json")
	request := ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}
	if _, err = PlanConfig(request); err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"foreign":true}`)
	os.WriteFile(target, changed, 0600)
	if _, err = ApplyConfig(plan); err == nil {
		t.Fatal("conflict overwritten")
	}
	actual, _ := os.ReadFile(target)
	if string(actual) != string(changed) {
		t.Fatal("conflict changed config")
	}
	os.Remove(plan)
	os.Remove(target)
	os.Symlink("absent", target)
	if _, err = PlanConfig(request); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestB07ConfigTamperedNoopPlanCannotClaimRegistration(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "config.json")
	os.WriteFile(target, []byte(`{}`), 0600)
	path := filepath.Join(root, "plan.json")
	_, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var plan configPlan
	json.Unmarshal(raw, &plan)
	plan.After = plan.Before
	raw, _ = json.Marshal(plan)
	os.WriteFile(path, raw, 0600)
	if _, err = ApplyConfig(path); err == nil {
		t.Fatal("tampered no-op claimed applied registration")
	}
	body, _ := os.ReadFile(target)
	if string(body) != "{}" {
		t.Fatal("tampered config modified")
	}
}
func TestB07ConfigCrossFilePendingReceiptRepair(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "config.json")
	os.WriteFile(target, []byte(`{}`), 0600)
	path := filepath.Join(root, "plan.json")
	_, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var plan configPlan
	json.Unmarshal(raw, &plan)
	// Model the durable pending receipt followed by config replace, before the
	// final applied-receipt write. Recovery must finish this original plan only.
	receipt := configReceipt{SchemaVersion: 1, TargetType: plan.Request.TargetType, Target: target, VersionRoot: result.VersionRoot, Registration: plan.Registration, PayloadSHA256: plan.PayloadSHA256, InstalledSHA256: plan.InstalledSHA256, Status: "pending", Action: "apply", Before: plan.Before, After: plan.After}
	raw, _ = json.Marshal(receipt)
	os.WriteFile(target+".agent-bird-receipt.json", raw, 0600)
	os.WriteFile(target, plan.After, 0600)
	if _, err = ApplyConfig(path); err != nil {
		t.Fatal(err)
	}
	got, _, err := configReadReceipt(target)
	if err != nil || got.Status != "applied" {
		t.Fatalf("pending receipt not repaired %v", err)
	}
}

func TestB07UninstallRefusesActiveRegisteredConfig(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	plan := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(plan); err != nil {
		t.Fatal(err)
	}
	if _, err = UninstallPackage(dest, "1.0.0"); err == nil {
		t.Fatal("uninstall removed assets with active Hook config")
	}
	if _, err = os.Stat(filepath.Join(result.VersionRoot, "scripts/agent-bird-agy-hook")); err != nil {
		t.Fatal("active assets lost")
	}
	remove := filepath.Join(root, "remove.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: remove, Action: "remove"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(remove); err != nil {
		t.Fatal(err)
	}
	if _, err = UninstallPackage(dest, "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestB07ChangedOwnedObjectRefusesRemoveAndKeepsUnrelatedOrder(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	before := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"first"}]},{"hooks":[{"type":"command","command":"second"}]}]},"private":"keep"}`)
	os.WriteFile(target, before, 0600)
	plan := filepath.Join(root, "plan.json")
	request := ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}
	if _, err = PlanConfig(request); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(plan); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	var settings map[string]json.RawMessage
	json.Unmarshal(raw, &settings)
	var hooks map[string][]json.RawMessage
	json.Unmarshal(settings["hooks"], &hooks)
	if len(hooks["UserPromptSubmit"]) != 3 || !strings.Contains(string(hooks["UserPromptSubmit"][0]), "first") || !strings.Contains(string(hooks["UserPromptSubmit"][1]), "second") {
		t.Fatal("unrelated Hook order changed")
	}
	hooks["UserPromptSubmit"][2] = []byte(`{"hooks":[{"type":"command","command":"foreign-changed"}]}`)
	settings["hooks"], _ = json.Marshal(hooks)
	changed, _ := json.Marshal(settings)
	os.WriteFile(target, changed, 0600)
	request.Action = "remove"
	request.PlanPath = filepath.Join(root, "remove.json")
	if _, err = PlanConfig(request); err == nil {
		t.Fatal("changed owned entry removed")
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(changed) {
		t.Fatal("foreign config overwritten")
	}
}

func TestB07RemovedConfigFixtureMayBeCleanedBeforeAssetUninstall(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "config")
	os.Mkdir(parent, 0700)
	target := filepath.Join(parent, "settings.json")
	plan := filepath.Join(parent, "apply.json")
	request := ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}
	if _, err = PlanConfig(request); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(plan); err != nil {
		t.Fatal(err)
	}
	request.PlanPath = filepath.Join(parent, "remove.json")
	request.Action = "remove"
	if _, err = PlanConfig(request); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(request.PlanPath); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(parent)
	if _, err = UninstallPackage(dest, "1.0.0"); err != nil {
		t.Fatalf("removed owned config still blocks uninstall: %v", err)
	}
}

func TestB07UnknownNullableUserSettingPreserved(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	os.WriteFile(target, []byte(`{"unrelated":null,"hooks":{}}`), 0600)
	plan := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(plan); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	var settings map[string]json.RawMessage
	json.Unmarshal(raw, &settings)
	if string(settings["unrelated"]) != "null" {
		t.Fatal("unrelated nullable setting lost")
	}
}
func TestB07StrictConfigInputsRefuseWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{"setting":1,"setting":2}`, `null`, `[]`, `{"hooks":null}`, `{"hooks":{"SessionStart":null}}`, strings.Repeat(" ", 1024*1024+1)} {
		target := filepath.Join(root, "target.json")
		os.WriteFile(target, []byte(body), 0600)
		plan := filepath.Join(root, "plan-invalid.json")
		if _, err = PlanConfig(ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}); err == nil {
			t.Fatal("invalid existing config accepted")
		}
		got, _ := os.ReadFile(target)
		if string(got) != body {
			t.Fatal("invalid user input rewritten")
		}
	}
	for _, body := range []string{`{"target_type":"agy-hooks","target_type":"claude-settings"}`, `{"unexpected":true}`, `{"target_type":null}`, `{"target_type":[]}`, strings.Repeat(" ", 1024*1024+1)} {
		path := filepath.Join(root, "request.json")
		os.WriteFile(path, []byte(body), 0600)
		var request ConfigPlanRequest
		if err = ReadConfigRequest(path, &request); err == nil {
			t.Fatal("invalid admin request accepted")
		}
	}
}

func TestB07StrictPlanInputsRefuseWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	before := []byte(`{"unrelated":"keep"}`)
	os.WriteFile(target, before, 0600)
	path := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	for _, change := range []string{"unknown", "duplicate", "null", "type", "size"} {
		var value map[string]json.RawMessage
		json.Unmarshal(original, &value)
		var body []byte
		switch change {
		case "unknown":
			value["unexpected"] = []byte(`true`)
		case "null":
			value["after"] = []byte(`null`)
		case "type":
			value["schema_version"] = []byte(`"1"`)
		}
		body, _ = json.Marshal(value)
		if change == "duplicate" {
			body = append([]byte(`{"schema_version":1,`), body[1:]...)
		}
		if change == "size" {
			body = append(body, []byte(strings.Repeat(" ", 1024*1024))...)
		}
		os.WriteFile(path, body, 0600)
		if _, err = ApplyConfig(path); err == nil {
			t.Fatalf("accepted %s plan", change)
		}
		got, _ := os.ReadFile(target)
		if string(got) != string(before) {
			t.Fatalf("%s plan overwrote target", change)
		}
	}
}

func TestB07PlanMissingRequiredBeforeRefusesNewTarget(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "new-settings.json")
	path := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var value map[string]json.RawMessage
	json.Unmarshal(raw, &value)
	delete(value, "before")
	raw, _ = json.Marshal(value)
	os.WriteFile(path, raw, 0600)
	if _, err = ApplyConfig(path); err == nil {
		t.Fatal("missing required plan field accepted")
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("invalid plan created target")
	}
}

func TestB07ConfigInitialStatesRepeatApplyRemove(t *testing.T) {
	for _, kind := range []string{"claude-settings", "agy-hooks"} {
		for _, initial := range []string{"missing", "empty-object", "unknown-null"} {
			t.Run(kind+"/"+initial, func(t *testing.T) {
				root := t.TempDir()
				os.Chmod(root, 0700)
				source := b07Payload(t, root)
				dest := filepath.Join(root, "installed")
				result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "settings.json")
				if initial != "missing" {
					body := `{}`
					if initial == "unknown-null" {
						body = `{"unrelated":null}`
					}
					os.WriteFile(target, []byte(body), 0600)
				}
				plan := filepath.Join(root, "apply.json")
				request := ConfigPlanRequest{TargetType: kind, Target: target, VersionRoot: result.VersionRoot, PlanPath: plan, Action: "apply"}
				if _, err = PlanConfig(request); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if _, err = ApplyConfig(plan); err != nil {
						t.Fatal(err)
					}
				}
				request.Action = "remove"
				request.PlanPath = filepath.Join(root, "remove.json")
				if _, err = PlanConfig(request); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if _, err = ApplyConfig(request.PlanPath); err != nil {
						t.Fatal(err)
					}
				}
				body, _ := os.ReadFile(target)
				var settings map[string]json.RawMessage
				if json.Unmarshal(body, &settings) != nil {
					t.Fatal("removed target invalid")
				}
				if initial == "unknown-null" && string(settings["unrelated"]) != "null" {
					t.Fatal("unknown nullable user setting lost")
				}
				if _, exists := settings["codex-orchestrator-agy-bridge"]; exists {
					t.Fatal("owned AGY registration retained")
				}
				if _, err = UninstallPackage(dest, "1.0.0"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestB07ForeignBackupCollisionRefusesWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	before := []byte(`{"unrelated":"keep"}`)
	os.WriteFile(target, before, 0600)
	backup := target + ".agent-bird-backup"
	foreign := []byte("foreign private backup")
	os.WriteFile(backup, foreign, 0600)
	path := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(path); err == nil {
		t.Fatal("foreign backup adopted and overwritten")
	}
	actual, _ := os.ReadFile(backup)
	if string(actual) != string(foreign) {
		t.Fatal("foreign backup overwritten")
	}
	actual, _ = os.ReadFile(target)
	if string(actual) != string(before) {
		t.Fatal("conflicted target overwritten")
	}
}

func TestB07SameBytesForeignBackupRefuses(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := b07Payload(t, root)
	dest := filepath.Join(root, "installed")
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "settings.json")
	before := []byte(`{}`)
	os.WriteFile(target, before, 0600)
	backup := target + ".agent-bird-backup"
	os.WriteFile(backup, before, 0600)
	old, _ := os.Stat(backup)
	path := filepath.Join(root, "plan.json")
	if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: result.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyConfig(path); err == nil {
		t.Fatal("same-bytes foreign backup adopted")
	}
	now, _ := os.Stat(backup)
	body, _ := os.ReadFile(backup)
	if !os.SameFile(old, now) || old.ModTime() != now.ModTime() || now.Mode().Perm() != 0600 || string(body) != string(before) {
		t.Fatal("foreign backup identity/content/mode changed")
	}
	body, _ = os.ReadFile(target)
	if string(body) != string(before) {
		t.Fatal("target changed")
	}
}

func TestB07ConfigSidecarConflictsPreserveFiles(t *testing.T) {
	for _, which := range []string{"backup-same", "backup-foreign", "receipt", "registry", "backup-link", "receipt-link", "lock-link"} {
		t.Run(which, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "config.json")
			before := []byte(`{}`)
			os.WriteFile(target, before, 0600)
			planPath := filepath.Join(root, "plan.json")
			request := ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: installed.VersionRoot, PlanPath: planPath, Action: "apply"}
			if which != "lock-link" {
				if _, err = PlanConfig(request); err != nil {
					t.Fatal(err)
				}
			}
			side := target + ".agent-bird-backup"
			foreign := []byte("foreign")
			if which == "backup-same" {
				foreign = before
			}
			if strings.HasPrefix(which, "receipt") {
				side = target + ".agent-bird-receipt.json"
			}
			if which == "registry" {
				dir := filepath.Join(dest, "config-registrations")
				os.Mkdir(dir, 0700)
				side = filepath.Join(dir, "1.0.0-"+configDigest([]byte(target))+".json")
			}
			if which == "lock-link" {
				side = target + ".agent-bird-lock"
			}
			if strings.HasSuffix(which, "link") {
				other := filepath.Join(root, "foreign.json")
				os.WriteFile(other, foreign, 0600)
				os.Symlink(other, side)
			} else {
				os.WriteFile(side, foreign, 0600)
			}
			old, _ := os.Lstat(side)
			if which == "lock-link" {
				_, err = PlanConfig(request)
			} else {
				_, err = ApplyConfig(planPath)
			}
			if err == nil {
				t.Fatal("sidecar collision accepted")
			}
			now, _ := os.Lstat(side)
			body, _ := os.ReadFile(side)
			if !os.SameFile(old, now) || old.ModTime() != now.ModTime() || old.Mode() != now.Mode() || string(body) != string(foreign) {
				t.Fatal("foreign sidecar changed")
			}
			body, _ = os.ReadFile(target)
			if string(body) != string(before) {
				t.Fatal("target changed")
			}
		})
	}
}
func TestB07ConfigPendingStageRepair(t *testing.T) {
	for _, stage := range []string{"before-backup", "after-backup", "after-target"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "config.json")
			os.WriteFile(target, []byte(`{}`), 0600)
			path := filepath.Join(root, "plan.json")
			if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: installed.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			var plan configPlan
			json.Unmarshal(raw, &plan)
			receipt := configReceipt{SchemaVersion: 1, TargetType: plan.Request.TargetType, Target: target, VersionRoot: installed.VersionRoot, Registration: plan.Registration, PayloadSHA256: plan.PayloadSHA256, InstalledSHA256: plan.InstalledSHA256, Status: "pending", Action: "apply", Before: plan.Before, After: plan.After}
			raw, _ = json.Marshal(receipt)
			os.WriteFile(target+".agent-bird-receipt.json", raw, 0600)
			if stage != "before-backup" {
				os.WriteFile(target+".agent-bird-backup", plan.Before, 0600)
			}
			if stage == "after-target" {
				os.WriteFile(target, plan.After, 0600)
			}
			if _, err = ApplyConfig(path); err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(target)
			if string(body) != string(plan.After) {
				t.Fatal("target not repaired")
			}
			r, _, err := configReadReceipt(target)
			if err != nil || r.Status != "applied" {
				t.Fatal("receipt not repaired", err)
			}
			if _, err = ApplyConfig(path); err != nil {
				t.Fatal("repeat failed", err)
			}
			if _, err = UninstallPackage(dest, "1.0.0"); err == nil {
				t.Fatal("repaired active config lost registry uninstall guard")
			}
		})
	}
}

func TestB07ReviewRegistryGuards(t *testing.T) {
	for _, which := range []string{"schema2", "directory-link"} {
		t.Run(which, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "config.json")
			before := []byte(`{}`)
			os.WriteFile(target, before, 0600)
			path := filepath.Join(root, "plan.json")
			if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: installed.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			var plan configPlan
			json.Unmarshal(raw, &plan)
			dir := filepath.Join(dest, "config-registrations")
			outside := filepath.Join(root, "outside")
			os.Mkdir(outside, 0700)
			sentinel := filepath.Join(outside, "sentinel")
			os.WriteFile(sentinel, []byte("preserve"), 0600)
			old, _ := os.Stat(sentinel)
			registry := filepath.Join(dir, "1.0.0-"+configDigest([]byte(target))+".json")
			if which == "directory-link" {
				os.Symlink(outside, dir)
			} else {
				os.Mkdir(dir, 0700)
				record := configRegistrationRecord{SchemaVersion: 2, Target: target, VersionRoot: installed.VersionRoot, PayloadSHA256: plan.PayloadSHA256, InstalledSHA256: plan.InstalledSHA256, TargetType: plan.Request.TargetType, Registration: plan.Registration}
				raw, _ = json.Marshal(record)
				os.WriteFile(registry, raw, 0600)
			}
			if _, err = ApplyConfig(path); err == nil {
				t.Fatal("foreign registry accepted")
			}
			body, _ := os.ReadFile(target)
			if string(body) != string(before) {
				t.Fatal("target changed")
			}
			now, _ := os.Stat(sentinel)
			body, _ = os.ReadFile(sentinel)
			if !os.SameFile(old, now) || now.Mode().Perm() != 0600 || string(body) != "preserve" {
				t.Fatal("outside bytes/identity/mode changed")
			}
			if which == "directory-link" {
				entries, _ := os.ReadDir(outside)
				if len(entries) != 1 {
					t.Fatal("outside registry created")
				}
			}
		})
	}
}
func TestB07ReviewKnownHookContainerTypes(t *testing.T) {
	for _, body := range []string{`{"hooks":{"SessionStart":[null]}}`, `{"hooks":{"SessionStart":[3]}}`, `{"hooks":{"SessionStart":[{"hooks":null}]}}`, `{"hooks":{"SessionStart":[{"hooks":{}}]}}`, `{"hooks":{"SessionStart":[{"hooks":[null]}]}}`, `{"hooks":{"SessionStart":[{"hooks":[3]}]}}`, `{"hooks":{"SessionStart":[{}]}}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "settings.json")
			os.WriteFile(target, []byte(body), 0600)
			old, _ := os.Stat(target)
			if _, err = PlanConfig(ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: installed.VersionRoot, PlanPath: filepath.Join(root, "plan.json"), Action: "apply"}); err == nil {
				t.Fatal("malformed operated Hook container accepted")
			}
			actual, _ := os.ReadFile(target)
			now, _ := os.Stat(target)
			if string(actual) != body || !os.SameFile(old, now) || now.Mode().Perm() != 0600 {
				t.Fatal("rejected target changed")
			}
		})
	}
}

func TestB07ReviewRemovedMalformedBypassesRefuse(t *testing.T) {
	for _, body := range []string{`null`, `{"hooks":null}`, `{"hooks":{"SessionStart":[null]}}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "config.json")
			for _, action := range []string{"apply", "remove"} {
				path := filepath.Join(root, action+".json")
				if _, err = PlanConfig(ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: installed.VersionRoot, PlanPath: path, Action: action}); err != nil {
					t.Fatal(err)
				}
				if _, err = ApplyConfig(path); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := os.ReadFile(filepath.Join(root, "remove.json"))
			var plan configPlan
			json.Unmarshal(raw, &plan)
			os.WriteFile(target, []byte(body), 0600)
			old, _ := os.Stat(target)
			if _, err = PlanConfig(ConfigPlanRequest{TargetType: "claude-settings", Target: target, VersionRoot: installed.VersionRoot, PlanPath: filepath.Join(root, "repeat.json"), Action: "remove"}); err == nil {
				t.Error("removed malformed target got valid plan")
			}
			plan.Before = []byte(body)
			plan.After = []byte(body)
			plan.Request.PlanPath = filepath.Join(root, "completed.json")
			_, plan.Identity, _ = configRead(target)
			raw, _ = json.Marshal(plan)
			os.WriteFile(plan.Request.PlanPath, raw, 0600)
			if _, err = ApplyConfig(plan.Request.PlanPath); err == nil {
				t.Error("completed removed fast path accepted malformed target")
			}
			now, _ := os.Stat(target)
			actual, _ := os.ReadFile(target)
			if !os.SameFile(old, now) || string(actual) != body {
				t.Fatal("malformed target changed")
			}
		})
	}
}

func TestB07ReviewAGYContainersAndUnknownNull(t *testing.T) {
	reg := map[string]json.RawMessage{"codex-orchestrator-agy-bridge": json.RawMessage(`{"PreInvocation":[{"type":"command","command":"owned"}]}`)}
	for _, body := range []string{`{"codex-orchestrator-agy-bridge":null}`, `{"codex-orchestrator-agy-bridge":{"PreInvocation":null}}`, `{"codex-orchestrator-agy-bridge":{"PreInvocation":{}}}`, `{"codex-orchestrator-agy-bridge":{"PreInvocation":[null]}}`, `{"codex-orchestrator-agy-bridge":{"PreInvocation":[3]}}`} {
		if _, err := configTransform([]byte(body), "agy-hooks", "apply", reg); err == nil {
			t.Fatal("invalid AGY container accepted", body)
		}
	}
	body := []byte(`{"unrelated":null,"hooks":{"SessionStart":[{"extra":null,"hooks":[{"type":"command","command":"third-party","extra":null}]}]}}`)
	claude := map[string]json.RawMessage{"SessionStart": json.RawMessage(`{"hooks":[{"type":"command","command":"owned"}]}`)}
	after, err := configTransform(body, "claude-settings", "apply", claude)
	if err != nil {
		t.Fatal("unknown null rejected", err)
	}
	removed, err := configTransform(after, "claude-settings", "remove", claude)
	if err != nil || !configEqual(body, removed) {
		t.Fatal("unknown values or original Hook order lost", err)
	}
}
func TestB07ReviewRegistryDirectoryAllConsumersRefuse(t *testing.T) {
	for _, call := range []string{"uninstall", "record", "forget"} {
		t.Run(call, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			source := b07Payload(t, root)
			dest := filepath.Join(root, "installed")
			installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Join(root, "state"), Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "config.json")
			path := filepath.Join(root, "plan.json")
			if _, err = PlanConfig(ConfigPlanRequest{TargetType: "agy-hooks", Target: target, VersionRoot: installed.VersionRoot, PlanPath: path, Action: "apply"}); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			var plan configPlan
			json.Unmarshal(raw, &plan)
			outside := filepath.Join(root, "outside")
			os.Mkdir(outside, 0700)
			os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep"), 0600)
			os.Symlink(outside, filepath.Join(dest, "config-registrations"))
			switch call {
			case "uninstall":
				_, err = UninstallPackage(dest, "1.0.0")
			case "record":
				err = configRecordRegistration(plan)
			case "forget":
				err = configForgetRegistration(plan)
			}
			if err == nil {
				t.Fatal("registry symlink accepted", call)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 1 {
				t.Fatal("outside changed")
			}
			if _, err = os.Stat(installed.VersionRoot); err != nil {
				t.Fatal("install deleted")
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("missing target changed")
			}
		})
	}
}
