package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

// These operations target explicitly owned files only. Advisory flock protects
// cooperative writers; the final expected-byte/identity check is not an atomic
// CAS against a foreign writer renaming a file between check and replacement.
type ConfigPlanRequest struct {
	TargetType  string `json:"target_type"`
	Target      string `json:"target"`
	VersionRoot string `json:"version_root"`
	PlanPath    string `json:"plan_path"`
	Action      string `json:"action"`
}
type configIdentity struct {
	Missing  bool   `json:"missing"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}
type configPlan struct {
	SchemaVersion   int                        `json:"schema_version"`
	Request         ConfigPlanRequest          `json:"request"`
	Before          []byte                     `json:"before"`
	After           []byte                     `json:"after"`
	Identity        configIdentity             `json:"identity"`
	Registration    map[string]json.RawMessage `json:"registration"`
	ReceiptBefore   []byte                     `json:"receipt_before"`
	PayloadSHA256   string                     `json:"payload_sha256"`
	InstalledSHA256 string                     `json:"installed_sha256"`
}
type configReceipt struct {
	SchemaVersion   int                        `json:"schema_version"`
	TargetType      string                     `json:"target_type"`
	Target          string                     `json:"target"`
	VersionRoot     string                     `json:"version_root"`
	Registration    map[string]json.RawMessage `json:"registration"`
	PayloadSHA256   string                     `json:"payload_sha256"`
	InstalledSHA256 string                     `json:"installed_sha256"`
	Status          string                     `json:"status"`
	Action          string                     `json:"action"`
	Before          []byte                     `json:"before"`
	After           []byte                     `json:"after"`
}
type ConfigResult struct {
	Status       string `json:"status"`
	Target       string `json:"target"`
	PlanPath     string `json:"plan_path"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
	ReceiptPath  string `json:"receipt_path"`
}

func configDigest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func configResult(plan configPlan, status string) ConfigResult {
	return ConfigResult{Status: status, Target: plan.Request.Target, PlanPath: plan.Request.PlanPath, BeforeSHA256: configDigest(plan.Before), AfterSHA256: configDigest(plan.After), ReceiptPath: plan.Request.Target + ".agent-bird-receipt.json"}
}
func configPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("config_path_invalid")
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("config_path_unsafe")
	}
	return verifyPrivateDirectory(parent)
}

// ReadConfigRequest enforces the same bounded private-file rules as plans.
func ReadConfigRequest(path string, target any) error {
	body, id, err := configRead(path)
	if err != nil {
		return err
	}
	if id.Missing {
		return errors.New("config_request_missing")
	}
	return strictJSON(body, target)
}
func configRead(path string) ([]byte, configIdentity, error) {
	if err := configPath(path); err != nil {
		return nil, configIdentity{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, configIdentity{Missing: true}, nil
	}
	if err != nil {
		return nil, configIdentity{}, err
	}
	body, err := readOwnedRegular(path, 0600, 1024*1024)
	if err != nil {
		return nil, configIdentity{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return nil, configIdentity{}, errors.New("config_conflict")
	}
	st := after.Sys().(*syscall.Stat_t)
	return body, configIdentity{Device: uint64(st.Dev), Inode: uint64(st.Ino), Size: after.Size(), Modified: after.ModTime().UnixNano()}, nil
}
func configLock(target string) (*os.File, error) {
	if err := configPath(target); err != nil {
		return nil, err
	}
	path := target + ".agent-bird-lock"
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("config_lock_unsafe")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("config_locked")
	}
	return f, nil
}
func configUnlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func configInstalled(root string) (string, string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(filepath.Dir(root)) != "versions" {
		return "", "", errors.New("config_install_invalid")
	}
	dest := filepath.Dir(filepath.Dir(root))
	if err := verifyInstalledVersion(dest, filepath.Base(root)); err != nil {
		return "", "", err
	}
	if kind, err := validatePayload(root, true); err != nil || kind == "" {
		return "", "", errors.New("config_hook_unavailable")
	}
	payload, err := hashOwnedRuntimeFile(filepath.Join(root, payloadManifestName), 0600, 1024*1024)
	if err != nil {
		return "", "", err
	}
	installed, err := hashOwnedRuntimeFile(filepath.Join(root, "manifest.json"), 0600, 1024*1024)
	return payload, installed, err
}
func configRegistration(kind, root string) (map[string]json.RawMessage, error) {
	reg := map[string]json.RawMessage{}
	switch kind {
	case "claude-settings":
		handler := map[string]any{"type": "command", "command": "/usr/bin/env", "args": []string{"CLAUDE_PLUGIN_ROOT=" + root, filepath.Join(root, "scripts/agent-bird-hook")}, "timeout": 3}
		group, _ := json.Marshal(map[string]any{"hooks": []any{handler}})
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"} {
			reg[event] = group
		}
	case "agy-hooks":
		entry := filepath.Join(root, "scripts/agent-bird-agy-hook")
		quoted := "'" + strings.ReplaceAll(entry, "'", "'\\''") + "'"
		object, _ := json.Marshal(map[string]any{"PreInvocation": []any{map[string]any{"type": "command", "command": quoted, "timeout": 3}}})
		reg["codex-orchestrator-agy-bridge"] = object
	default:
		return nil, errors.New("config_target_type_invalid")
	}
	return reg, nil
}
func configRegEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if !configEqual(value, b[key]) {
			return false
		}
	}
	return true
}
func configEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// Validate only containers this product operates. Unrelated values, including
// null and unknown handler fields, retain their original JSON semantics.
func configHandlerObjects(raw json.RawMessage) bool {
	var handlers []json.RawMessage
	if json.Unmarshal(raw, &handlers) != nil || handlers == nil {
		return false
	}
	for _, handler := range handlers {
		var object map[string]json.RawMessage
		if json.Unmarshal(handler, &object) != nil || object == nil {
			return false
		}
	}
	return true
}
func configDecodeUser(body []byte, kind string, missing bool) (map[string]json.RawMessage, error) {
	current := map[string]json.RawMessage{}
	if len(body) == 0 {
		if missing {
			return current, nil
		}
		return nil, errors.New("config_json_invalid")
	}
	if strictUserJSON(body, &current) != nil || current == nil {
		return nil, errors.New("config_json_invalid")
	}
	if kind == "agy-hooks" {
		if raw, ok := current["codex-orchestrator-agy-bridge"]; ok {
			var hook map[string]json.RawMessage
			if json.Unmarshal(raw, &hook) != nil || hook == nil || !configHandlerObjects(hook["PreInvocation"]) {
				return nil, errors.New("config_shape_invalid")
			}
		}
	} else if kind == "claude-settings" {
		var hooks map[string]json.RawMessage
		if raw, ok := current["hooks"]; ok {
			if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
				return nil, errors.New("config_shape_invalid")
			}
			for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"} {
				if raw, exists := hooks[event]; exists {
					var groups []json.RawMessage
					if json.Unmarshal(raw, &groups) != nil || groups == nil {
						return nil, errors.New("config_shape_invalid")
					}
					for _, raw := range groups {
						var group map[string]json.RawMessage
						if json.Unmarshal(raw, &group) != nil || group == nil || !configHandlerObjects(group["hooks"]) {
							return nil, errors.New("config_shape_invalid")
						}
					}
				}
			}
		}
	} else {
		return nil, errors.New("config_target_invalid")
	}
	return current, nil
}

// Unrelated values and Hook list order survive; whitespace is normalized.
func configTransform(body []byte, kind, action string, reg map[string]json.RawMessage) ([]byte, error) {
	current, decodeErr := configDecodeUser(body, kind, len(body) == 0)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if kind == "agy-hooks" {
		for key, want := range reg {
			got, exists := current[key]
			if action == "apply" {
				if exists {
					return nil, errors.New("config_registration_conflict")
				}
				current[key] = want
			} else {
				if !exists || !configEqual(got, want) {
					return nil, errors.New("config_registration_conflict")
				}
				delete(current, key)
			}
		}
	} else {
		hooks := map[string]json.RawMessage{}
		if raw, ok := current["hooks"]; ok {
			if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
				return nil, errors.New("config_shape_invalid")
			}
		}
		for event, want := range reg {
			var groups []json.RawMessage
			raw, exists := hooks[event]
			if exists && (json.Unmarshal(raw, &groups) != nil || groups == nil) {
				return nil, errors.New("config_shape_invalid")
			}
			index := -1
			for i, got := range groups {
				if configEqual(got, want) {
					if index != -1 {
						return nil, errors.New("config_registration_conflict")
					}
					index = i
				}
			}
			if action == "apply" {
				if index != -1 {
					return nil, errors.New("config_registration_conflict")
				}
				groups = append(groups, want)
			} else {
				if index < 0 {
					return nil, errors.New("config_registration_conflict")
				}
				groups = append(groups[:index], groups[index+1:]...)
			}
			if len(groups) == 0 {
				delete(hooks, event)
			} else {
				hooks[event], _ = json.Marshal(groups)
			}
		}
		current["hooks"], _ = json.Marshal(hooks)
	}
	out, err := json.MarshalIndent(current, "", "  ")
	return append(out, '\n'), err
}
func configHasRegistration(body []byte, kind string, reg map[string]json.RawMessage) bool {
	_, err := configTransform(body, kind, "remove", reg)
	return err == nil
}
func configReadReceipt(target string) (configReceipt, []byte, error) {
	body, identity, err := configRead(target + ".agent-bird-receipt.json")
	if err != nil {
		return configReceipt{}, nil, err
	}
	if identity.Missing {
		return configReceipt{}, []byte{}, nil
	}
	var r configReceipt
	if strictJSON(body, &r) != nil || r.SchemaVersion != 1 || r.Target != target || (r.Status != "pending" && r.Status != "applied" && r.Status != "removed") {
		return r, nil, errors.New("config_receipt_invalid")
	}
	return r, body, nil
}
func PlanConfig(request ConfigPlanRequest) (ConfigResult, error) {
	if request.Action != "apply" && request.Action != "remove" {
		return ConfigResult{}, errors.New("config_action_invalid")
	}
	if request.PlanPath == request.Target || strings.HasPrefix(request.PlanPath, request.Target+".agent-bird-") {
		return ConfigResult{}, errors.New("config_plan_path_invalid")
	}
	if err := configPath(request.PlanPath); err != nil {
		return ConfigResult{}, err
	}
	lock, err := configLock(request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	defer configUnlock(lock)
	installLock, err := configInstallLock(request.VersionRoot)
	if err != nil {
		return ConfigResult{}, err
	}
	defer releaseInstallLock(installLock)
	payload, installed, err := configInstalled(request.VersionRoot)
	if err != nil {
		return ConfigResult{}, err
	}
	reg, err := configRegistration(request.TargetType, request.VersionRoot)
	if err != nil {
		return ConfigResult{}, err
	}
	before, identity, err := configRead(request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	if _, err = configDecodeUser(before, request.TargetType, identity.Missing); err != nil {
		return ConfigResult{}, err
	}
	receipt, receiptBefore, err := configReadReceipt(request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	after := before
	if len(receiptBefore) > 0 {
		if receipt.VersionRoot != request.VersionRoot || receipt.TargetType != request.TargetType || receipt.PayloadSHA256 != payload || receipt.InstalledSHA256 != installed || !configRegEqual(receipt.Registration, reg) {
			return ConfigResult{}, errors.New("config_receipt_conflict")
		}
		if receipt.Status == "pending" {
			return ConfigResult{}, errors.New("config_pending_apply_original_plan")
		}
	}
	if request.Action == "apply" {
		if len(receiptBefore) > 0 && receipt.Status == "applied" {
			if !configHasRegistration(before, request.TargetType, reg) {
				return ConfigResult{}, errors.New("config_registration_conflict")
			}
		} else {
			after, err = configTransform(before, request.TargetType, "apply", reg)
		}
	} else {
		if len(receiptBefore) == 0 {
			return ConfigResult{}, errors.New("config_receipt_required")
		}
		if receipt.Status == "removed" {
			if configHasRegistration(before, request.TargetType, reg) {
				return ConfigResult{}, errors.New("config_registration_conflict")
			}
		} else {
			after, err = configTransform(before, request.TargetType, "remove", reg)
		}
	}
	if err != nil {
		return ConfigResult{}, err
	}
	plan := configPlan{SchemaVersion: 1, Request: request, Before: before, After: after, Identity: identity, Registration: reg, ReceiptBefore: receiptBefore, PayloadSHA256: payload, InstalledSHA256: installed}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return ConfigResult{}, err
	}
	if err = writeExclusive(request.PlanPath, append(body, '\n'), 0600); err != nil {
		return ConfigResult{}, err
	}
	if err = syncDir(filepath.Dir(request.PlanPath)); err != nil {
		return ConfigResult{}, err
	}
	return configResult(plan, "planned"), nil
}
func configReplace(path string, body []byte) error {
	if err := configPath(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-bird-config-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// The pending receipt proves a backup created by this plan. Before writing that
// receipt, an existing backup requires the previous exact owned receipt instead.
func configCheckBackup(plan configPlan, pending bool) ([]byte, configIdentity, error) {
	body, identity, err := configRead(plan.Request.Target + ".agent-bird-backup")
	if err != nil || identity.Missing {
		return body, identity, err
	}
	if pending && bytes.Equal(body, plan.Before) {
		return body, identity, nil
	}
	var previous configReceipt
	if strictJSON(plan.ReceiptBefore, &previous) != nil || previous.SchemaVersion != 1 || previous.Target != plan.Request.Target || previous.TargetType != plan.Request.TargetType || previous.VersionRoot != plan.Request.VersionRoot || previous.PayloadSHA256 != plan.PayloadSHA256 || previous.InstalledSHA256 != plan.InstalledSHA256 || !configRegEqual(previous.Registration, plan.Registration) || (previous.Status != "applied" && previous.Status != "removed") || !bytes.Equal(body, previous.Before) {
		return nil, identity, errors.New("config_backup_conflict")
	}
	return body, identity, nil
}
func configSaveBackup(plan configPlan) error {
	path := plan.Request.Target + ".agent-bird-backup"
	body, identity, err := configCheckBackup(plan, true)
	if err != nil {
		return err
	}
	if identity.Missing {
		if err = writeExclusive(path, plan.Before, 0600); err != nil {
			return err
		}
		return syncDir(filepath.Dir(path))
	}
	if bytes.Equal(body, plan.Before) {
		return nil
	}
	latest, latestID, err := configRead(path)
	if err != nil || latestID != identity || !bytes.Equal(latest, body) {
		return errors.New("config_backup_conflict")
	}
	return configReplace(path, plan.Before)
}
func ApplyConfig(planPath string) (ConfigResult, error) { return ApplyConfigAction(planPath, "") }
func ApplyConfigAction(planPath, action string) (ConfigResult, error) {
	body, _, err := configRead(planPath)
	if err != nil {
		return ConfigResult{}, err
	}
	var plan configPlan
	if strictJSON(body, &plan) != nil || plan.SchemaVersion != 1 || plan.Request.PlanPath != planPath || (plan.Request.Action != "apply" && plan.Request.Action != "remove") || action != "" && action != plan.Request.Action {
		return ConfigResult{}, errors.New("config_plan_invalid")
	}
	lock, err := configLock(plan.Request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	defer configUnlock(lock)
	installLock, err := configInstallLock(plan.Request.VersionRoot)
	if err != nil {
		return ConfigResult{}, err
	}
	defer releaseInstallLock(installLock)
	payload, installed, err := configInstalled(plan.Request.VersionRoot)
	if err != nil || payload != plan.PayloadSHA256 || installed != plan.InstalledSHA256 {
		return ConfigResult{}, errors.New("config_install_changed")
	}
	reg, err := configRegistration(plan.Request.TargetType, plan.Request.VersionRoot)
	if err != nil || !configRegEqual(reg, plan.Registration) {
		return ConfigResult{}, errors.New("config_plan_invalid")
	}
	receipt, receiptBytes, err := configReadReceipt(plan.Request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	current, identity, err := configRead(plan.Request.Target)
	if err != nil {
		return ConfigResult{}, err
	}
	if _, err = configDecodeUser(current, plan.Request.TargetType, identity.Missing); err != nil {
		return ConfigResult{}, err
	}
	completed := "applied"
	if plan.Request.Action == "remove" {
		completed = "removed"
	}
	sameReceipt := receipt.VersionRoot == plan.Request.VersionRoot && receipt.TargetType == plan.Request.TargetType && receipt.PayloadSHA256 == payload && receipt.InstalledSHA256 == installed && configRegEqual(receipt.Registration, reg)
	if sameReceipt && receipt.Status == completed && receipt.Action == plan.Request.Action && bytes.Equal(current, plan.After) {
		if completed == "removed" {
			if err = configForgetRegistration(plan); err != nil {
				return configResult(plan, "pending_registry_repair"), err
			}
		}
		return configResult(plan, "already_"+completed), nil
	}
	if bytes.Equal(plan.Before, plan.After) {
		present := configHasRegistration(current, plan.Request.TargetType, reg)
		if !sameReceipt || receipt.Status != completed || (plan.Request.Action == "apply" && !present) || (plan.Request.Action == "remove" && present) {
			return ConfigResult{}, errors.New("config_plan_invalid")
		}
	}
	pending := sameReceipt && receipt.Status == "pending" && receipt.Action == plan.Request.Action && bytes.Equal(receipt.Before, plan.Before) && bytes.Equal(receipt.After, plan.After)
	if !pending && !bytes.Equal(receiptBytes, plan.ReceiptBefore) {
		return ConfigResult{}, errors.New("config_receipt_conflict")
	}
	if !bytes.Equal(current, plan.Before) || identity != plan.Identity {
		if !pending || !bytes.Equal(current, plan.After) {
			return ConfigResult{}, errors.New("config_conflict")
		}
		expected, e := configTransform(plan.Before, plan.Request.TargetType, plan.Request.Action, reg)
		if e != nil || !bytes.Equal(expected, plan.After) {
			return ConfigResult{}, errors.New("config_plan_invalid")
		}
		if _, _, err = configCheckBackup(plan, true); err != nil {
			return ConfigResult{}, err
		}
		if err = configRecordRegistration(plan); err != nil {
			return ConfigResult{}, err
		}
		if err = configSaveBackup(plan); err != nil {
			return ConfigResult{}, err
		}
	} else {
		expected, transformErr := configTransform(plan.Before, plan.Request.TargetType, plan.Request.Action, reg)
		if !bytes.Equal(plan.Before, plan.After) && (transformErr != nil || !bytes.Equal(expected, plan.After)) {
			return ConfigResult{}, errors.New("config_plan_invalid")
		}
		if _, _, err = configCheckBackup(plan, pending); err != nil {
			return ConfigResult{}, err
		}
		if err = configRecordRegistration(plan); err != nil {
			return ConfigResult{}, err
		}
		if !pending {
			receipt = configReceipt{SchemaVersion: 1, TargetType: plan.Request.TargetType, Target: plan.Request.Target, VersionRoot: plan.Request.VersionRoot, Registration: reg, PayloadSHA256: payload, InstalledSHA256: installed, Status: "pending", Action: plan.Request.Action, Before: plan.Before, After: plan.After}
			raw, _ := json.Marshal(receipt)
			if err = configReplace(plan.Request.Target+".agent-bird-receipt.json", raw); err != nil {
				return ConfigResult{}, err
			}
		}
		if err = configSaveBackup(plan); err != nil {
			return ConfigResult{}, err
		}
		// Check exact content and identity again immediately before replacement.
		latest, latestID, e := configRead(plan.Request.Target)
		if e != nil || latestID != identity || !bytes.Equal(latest, current) {
			return ConfigResult{}, errors.New("config_conflict")
		}
		if err = configReplace(plan.Request.Target, plan.After); err != nil {
			return ConfigResult{}, err
		}
	}
	receipt.Status = completed
	raw, _ := json.Marshal(receipt)
	if err = configReplace(plan.Request.Target+".agent-bird-receipt.json", raw); err != nil {
		return configResult(plan, "pending_receipt_repair"), err
	}
	if completed == "removed" {
		if err = configForgetRegistration(plan); err != nil {
			return configResult(plan, "pending_registry_repair"), err
		}
	}
	return configResult(plan, completed), nil
}

type configRegistrationRecord struct {
	SchemaVersion   int                        `json:"schema_version"`
	Target          string                     `json:"target"`
	VersionRoot     string                     `json:"version_root"`
	PayloadSHA256   string                     `json:"payload_sha256"`
	InstalledSHA256 string                     `json:"installed_sha256"`
	TargetType      string                     `json:"target_type"`
	Registration    map[string]json.RawMessage `json:"registration"`
}

func configInstallLock(root string) (*os.File, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(filepath.Dir(root)) != "versions" || !versionPattern.MatchString(filepath.Base(root)) {
		return nil, errors.New("config_install_invalid")
	}
	dest := filepath.Dir(filepath.Dir(root))
	if err := verifyPrivateDirectory(dest); err != nil {
		return nil, err
	}
	if _, _, err := configInstalled(root); err != nil {
		return nil, err
	}
	return acquireInstallLock(dest)
}
func configRegistryDirectory(destination string, create bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(destination)
	if err != nil || resolved != destination || verifyPrivateDirectory(destination) != nil {
		return "", errors.New("config_registry_unsafe")
	}
	directory := filepath.Join(destination, "config-registrations")
	if _, err = os.Lstat(directory); errors.Is(err, os.ErrNotExist) && create {
		err = os.Mkdir(directory, 0700)
	}
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory || verifyPrivateDirectory(directory) != nil {
		return "", errors.New("config_registry_unsafe")
	}
	return directory, nil
}
func configRecordRegistration(plan configPlan) error {
	dest := filepath.Dir(filepath.Dir(plan.Request.VersionRoot))
	directory, err := configRegistryDirectory(dest, true)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, filepath.Base(plan.Request.VersionRoot)+"-"+configDigest([]byte(plan.Request.Target))+".json")
	record := configRegistrationRecord{SchemaVersion: 1, Target: plan.Request.Target, VersionRoot: plan.Request.VersionRoot, PayloadSHA256: plan.PayloadSHA256, InstalledSHA256: plan.InstalledSHA256, TargetType: plan.Request.TargetType, Registration: plan.Registration}
	body, _ := json.Marshal(record)
	if _, err := os.Lstat(path); err == nil {
		raw, e := readOwnedRegular(path, 0600, 1024*1024)
		var old configRegistrationRecord
		if e != nil || strictJSON(raw, &old) != nil || old.SchemaVersion != 1 || old.Target != record.Target || old.VersionRoot != record.VersionRoot || old.PayloadSHA256 != record.PayloadSHA256 || old.InstalledSHA256 != record.InstalledSHA256 || old.TargetType != record.TargetType || !configRegEqual(old.Registration, record.Registration) {
			return errors.New("config_registry_conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeExclusive(path, body, 0600); err != nil {
		return err
	}
	return syncDir(directory)
}
func configCheckUninstall(destination, version string) error {
	directory, err := configRegistryDirectory(destination, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	root := filepath.Join(destination, "versions", version)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), version+"-") {
			continue
		}
		raw, e := readOwnedRegular(filepath.Join(directory, entry.Name()), 0600, 1024*1024)
		var record configRegistrationRecord
		if e != nil || strictJSON(raw, &record) != nil || record.SchemaVersion != 1 || record.VersionRoot != root || entry.Name() != version+"-"+configDigest([]byte(record.Target))+".json" {
			return errors.New("config_registry_invalid")
		}
		receipt, _, e := configReadReceipt(record.Target)
		if e != nil || receipt.Status != "removed" || receipt.VersionRoot != root || receipt.PayloadSHA256 != record.PayloadSHA256 || receipt.InstalledSHA256 != record.InstalledSHA256 || receipt.TargetType != record.TargetType || !configRegEqual(receipt.Registration, record.Registration) {
			return errors.New("config_registered")
		}
		current, _, e := configRead(record.Target)
		if e != nil || configHasRegistration(current, record.TargetType, record.Registration) || bytes.Contains(current, []byte(root)) {
			return errors.New("config_registered")
		}
	}
	return nil
}

// Forget only this exact registration after its removed receipt is durable.
// A failed registry cleanup retains a conservative uninstall block; retrying
// the original remove plan repairs it without touching the configuration.
func configForgetRegistration(plan configPlan) error {
	directory, err := configRegistryDirectory(filepath.Dir(filepath.Dir(plan.Request.VersionRoot)), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	path := filepath.Join(directory, filepath.Base(plan.Request.VersionRoot)+"-"+configDigest([]byte(plan.Request.Target))+".json")
	raw, id, err := configRead(path)
	if err != nil {
		return err
	}
	if id.Missing {
		return nil
	}
	var record configRegistrationRecord
	if strictJSON(raw, &record) != nil || record.SchemaVersion != 1 || record.Target != plan.Request.Target || record.VersionRoot != plan.Request.VersionRoot || record.PayloadSHA256 != plan.PayloadSHA256 || record.InstalledSHA256 != plan.InstalledSHA256 || record.TargetType != plan.Request.TargetType || !configRegEqual(record.Registration, plan.Registration) {
		return errors.New("config_registry_conflict")
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return syncDir(directory)
}
