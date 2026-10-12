package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type agySandboxFileOperation struct {
	Name, Kind, Path, Target, Body, Want, ReadSHA256 string
}
type agySandboxFilePlan struct {
	Operations []agySandboxFileOperation
}
type agySandboxFileObservation struct {
	Name, ErrorClass string
	Allowed, Match   bool
}
type agySandboxFileReport struct {
	PID          int
	Observations []agySandboxFileObservation
}

// Only the current test executable runs here: no native CLI, command runner,
// network, account, real HOME, or child process is used by the helper.
func TestAGYHistorySandboxFileAPIHelper(t *testing.T) {
	if os.Getenv("AGY_HISTORY_SANDBOX_HELPER") != "1" {
		t.Skip("owned sandbox helper only")
	}
	f, err := os.OpenFile(os.Getenv("AGY_HISTORY_SANDBOX_PLAN"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal("owned helper plan unavailable")
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	var plan agySandboxFilePlan
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &plan) != nil || len(plan.Operations) == 0 || len(plan.Operations) > 80 {
		t.Fatal("owned helper plan invalid")
	}
	report := agySandboxFileReport{PID: os.Getpid()}
	for _, op := range plan.Operations {
		var err error
		var read []byte
		switch op.Kind {
		case "read":
			read, err = os.ReadFile(op.Path)
		case "write":
			err = os.WriteFile(op.Path, []byte(op.Body), 0600)
		case "remove":
			err = os.Remove(op.Path)
		case "rename":
			err = os.Rename(op.Path, op.Target)
		case "mkdir":
			err = os.Mkdir(op.Path, 0700)
		default:
			t.Fatal("unknown owned helper operation")
		}
		class := "none"
		if err != nil {
			class = "other"
			if errors.Is(err, os.ErrPermission) {
				class = "permission"
			} else if errors.Is(err, os.ErrNotExist) {
				class = "missing"
			}
		}
		match := err == nil
		if op.Want == "deny" {
			match = class == "permission"
		} else if op.Kind == "read" {
			match = match && hashBytes(read) == op.ReadSHA256
		}
		report.Observations = append(report.Observations, agySandboxFileObservation{op.Name, class, err == nil, match})
	}
	body, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("AGY_SANDBOX_FILE_REPORT %s\n", body)
}

// This catches a missing owned write grant, an overbroad config/link grant,
// or a missing-config grant that permits settings initialization. Production
// bytes remain f49aa866; these opt-in tests establish kernel policy only.
func TestAGYHistorySandboxPermissions(t *testing.T) {
	if os.Getenv("AGY_HISTORY_SANDBOX_OPT_IN") != "1" {
		t.Skip("explicit owned macOS sandbox permission gate only")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("this gate requires the actual macOS sandbox")
	}
	parent, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	root, err := os.MkdirTemp(os.Getenv("TMPDIR"), "agy-sandbox-owned-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	unprovedHandles := 0
	t.Cleanup(func() {
		if unprovedHandles == 0 {
			if err := os.RemoveAll(root); err != nil {
				t.Error("proved owned fixture cleanup failed")
			}
		} else {
			t.Logf("UNKNOWN owned_fixture_preserved=%s", root)
		}
	})
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range []bool{true, false} {
		name := "missing_config_defaults"
		if existing {
			name = "existing_three_references"
		}
		t.Run(name, func(t *testing.T) {
			agySandboxPermissionCase(t, parent, parentDeadline, root, binary, name, existing, &unprovedHandles)
		})
		if unprovedHandles != 0 {
			t.Error("UNKNOWN original startup/tree proof incomplete; no later permission case may start")
			break
		}
	}
	if parent.Err() != nil {
		t.Error("40s original permission parent deadline exceeded")
	}
}

func agySandboxPermissionCase(t *testing.T, parent context.Context, parentDeadline time.Time, root, binary, name string, existing bool, unprovedHandles *int) {
	t.Helper()
	base := filepath.Join(root, name)
	home := filepath.Join(base, "home")
	gemini := filepath.Join(home, ".gemini")
	original := filepath.Join(gemini, "antigravity-cli")
	work := filepath.Join(base, "workspace")
	for _, dir := range []string{filepath.Join(original, "cache"), filepath.Join(gemini, "config", "projects"), filepath.Join(home, ".config"), work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := map[string]string{"settings.json": `{"modelProvider":"synthetic-original"}`, "keybindings.json": `{"synthetic":"binding"}`, "cache/default_project_id.txt": "synthetic-project\n"}
	protected := map[string]string{
		filepath.Join(gemini, "antigravity-oauth-token"):      "synthetic-auth-never-real",
		filepath.Join(home, ".config", "original.json"):       "synthetic-global-config",
		filepath.Join(gemini, "config", "projects", "p.json"): "synthetic-project-config",
	}
	if existing {
		for path, body := range config {
			protected[filepath.Join(original, path)] = body
		}
	}
	for path, body := range protected {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := NewIPC(filepath.Join(base, "state", "host-spool", "launch"), "sandbox-producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("sandbox-owner"); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000}
	// Synthetic payload selects the fixed app-data preparation branch. It is
	// not a provider admission/Probe. The actual launched test binary is pinned
	// independently below and no AGY executable is opened or run.
	pin := adapter.BinaryPin{Path: binary, Version: agyHistoryVersion, SHA256: agyHistorySHA256}
	payload, err := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: "antigravity-cli", Directory: work, Profile: profile,
		ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: pin}})
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{RunID: "sandbox-run", TaskID: name, AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, AdapterPayload: payload}
	scratch := filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "scratch")
	planPath := filepath.Join(scratch, "file-plan.json")
	cmd := process.Command{Path: binary, Dir: work, Args: []string{"-test.run=^TestAGYHistorySandboxFileAPIHelper$", "-test.timeout=5s", "--"}, ExactEnv: true,
		Env: []string{"HOME=" + home, "AGY_HISTORY_SANDBOX_HELPER=1", "AGY_HISTORY_SANDBOX_PLAN=" + planPath}}
	cmd, err = h.sandboxProfileCommand(parent, cmd, profile, scratch, grant)
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(gemini, agyHistoryArgument(t, cmd))
	var plan agySandboxFilePlan
	add := func(name, kind, path, target, body, want string) {
		plan.Operations = append(plan.Operations, agySandboxFileOperation{name, kind, path, target, body, want, hashText(body)})
	}
	conversations := filepath.Join(owned, "conversations")
	tmp := filepath.Join(conversations, ".9adeef5e-cd28-4c60-9858-a3462e408814.tmp")
	final := filepath.Join(conversations, "9adeef5e-cd28-4c60-9858-a3462e408814.pb")
	add("conversations_mkdir", "mkdir", conversations, "", "", "allow")
	for _, body := range []string{"first-native-fixture", "next-native-fixture"} {
		add(body+"_write", "write", tmp, "", body, "allow")
		add(body+"_atomic_save", "rename", tmp, final, "", "allow")
		add(body+"_read", "read", final, "", body, "allow")
	}
	replacement := filepath.Join(scratch, "replacement")
	add("scratch_replacement", "write", replacement, "", "replacement", "allow")
	for _, configName := range []string{"settings.json", "keybindings.json", "cache/default_project_id.txt"} {
		source, link := filepath.Join(original, configName), filepath.Join(owned, configName)
		if existing {
			for _, path := range []string{source, link} {
				add(configName+"_read", "read", path, "", config[configName], "allow")
				add(configName+"_write", "write", path, "", "forbidden", "deny")
				add(configName+"_remove", "remove", path, "", "", "deny")
				add(configName+"_replace", "rename", replacement, path, "", "deny")
			}
		} else {
			add(configName+"_original_create", "write", source, "", "forbidden", "deny")
			if configName == "cache/default_project_id.txt" {
				add("owned_mutable_index_create", "write", link, "", "synthetic-next-project", "allow")
				add("owned_mutable_index_read", "read", link, "", "synthetic-next-project", "allow")
			} else {
				add(configName+"_owned_create", "write", link, "", "forbidden", "deny")
			}
		}
	}
	if existing {
		add("readonly_index_parent_rename", "rename", filepath.Join(owned, "cache"), filepath.Join(owned, "cache-replaced"), "", "deny")
	}
	for _, path := range []string{filepath.Join(gemini, "antigravity-oauth-token"), filepath.Join(home, ".config", "original.json"), filepath.Join(gemini, "config", "projects", "p.json")} {
		add("original_scope_write", "write", path, "", "forbidden", "deny")
		add("original_scope_create", "write", filepath.Join(filepath.Dir(path), "forbidden-created"), "", "forbidden", "deny")
	}
	before := map[string]agyConfigReference{}
	for path := range protected {
		before[path], err = agyFileSnapshot(path, path, agyConfigLimit)
		if err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]continuationFileIdentity{}
	for configName := range config {
		if existing {
			path := filepath.Join(owned, configName)
			links[path], err = agyLinkIdentity(path, filepath.Join(original, configName))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	planBody, err := json.Marshal(plan)
	if err != nil || len(planBody) > 64<<10 || len(plan.Operations) > 80 {
		t.Fatal("owned file plan exceeds bound")
	}
	if err = os.WriteFile(planPath, planBody, 0600); err != nil {
		t.Fatal(err)
	}
	cmd.PinnedPath = binary
	cmd.PinnedSHA256, err = process.ExecutableDigest(parent, binary)
	if err != nil {
		t.Fatal(err)
	}
	op, cancelOp := context.WithTimeout(parent, 5*time.Second)
	defer cancelOp()
	opDeadline, _ := op.Deadline()
	cleanupDeadline := opDeadline.Add(10 * time.Second)
	// Reserve the original process.Stop implementation's existing 1s exit
	// confirmation inside the fixed parent boundary; never add a fresh grace.
	if last := parentDeadline.Add(-time.Second); cleanupDeadline.After(last) {
		cleanupDeadline = last
	}
	*unprovedHandles++
	// Start can spawn and then return nil/error after a failed birth probe.
	// Register uncertainty before that call; no returned original Handle means
	// no usable positive Wait/tree proof, even if its internal Stop attempted.
	child, err := process.Start(op, cmd)
	if err != nil || child == nil {
		t.Fatal("UNKNOWN process.Start did not provide an original Handle; preserve fixture and stop later cases")
	}
	identity := child.Identity()
	var operationWaitErr error
	operationWaitObserved := false
	defer func() {
		cleanup, cancelCleanup := context.WithDeadline(context.Background(), cleanupDeadline)
		defer cancelCleanup()
		waitErr := operationWaitErr
		if !operationWaitObserved {
			waitErr = child.Wait(op)
		}
		var stopErr error
		if waitErr != nil || child.ConfirmTreeExited() != nil {
			stopErr = child.Stop(cleanup)
			waitErr = child.Wait(cleanup)
			if stopErr != nil {
				t.Log("UNKNOWN original handle cleanup did not confirm Stop")
			}
		}
		groupAbsent := child.ConfirmTreeExited() == nil
		proved := waitErr == nil && groupAbsent && stopErr == nil && cleanup.Err() == nil
		if proved {
			*unprovedHandles--
		}
		t.Logf("sandbox_original_handle case=%s pid=%d pgid=%d birth=%s birth_known=%t original_wait=%t group_absent=%t bounded_cleanup_confirmed=%t stop_unknown=%t operations=%d parent_deadline_unix_ms=%d operation_deadline_unix_ms=%d cleanup_deadline_unix_ms=%d", name, identity.PID, identity.PGID, identity.Birth, identity.BirthKnown, waitErr == nil, groupAbsent, proved, stopErr != nil, len(plan.Operations), parentDeadline.UnixMilli(), opDeadline.UnixMilli(), cleanupDeadline.UnixMilli())
		if !proved {
			t.Error("UNKNOWN original Handle Wait/tree proof incomplete; no PID reconstruction or extra cleanup grace")
		}
	}()
	operationWaitErr = child.Wait(op)
	operationWaitObserved = true
	if operationWaitErr != nil {
		t.Fatal("5s sandbox file helper did not finish")
	}
	if child.ExitCode() != 0 {
		t.Fatalf("sandbox file helper failed: original exit=%d", child.ExitCode())
	}
	var report agySandboxFileReport
	found := false
	for _, line := range strings.Split(child.Output(), "\n") {
		if body, ok := strings.CutPrefix(line, "AGY_SANDBOX_FILE_REPORT "); ok {
			if found || json.Unmarshal([]byte(body), &report) != nil {
				t.Fatal("ambiguous sandbox helper report")
			}
			found = true
		}
	}
	if !found || report.PID != identity.PID || len(report.Observations) != len(plan.Operations) {
		t.Fatal("sandbox observations not bound to the original helper")
	}
	for i, observation := range report.Observations {
		if observation.Name != plan.Operations[i].Name || !observation.Match {
			t.Errorf("file policy mismatch operation=%s kind=%s want=%s allowed=%t error=%s", plan.Operations[i].Name, plan.Operations[i].Kind, plan.Operations[i].Want, observation.Allowed, observation.ErrorClass)
		}
	}
	for path, ref := range before {
		after, err := agyFileSnapshot(path, path, agyConfigLimit)
		if err != nil || ref != after {
			t.Error("original synthetic config/auth/project content or identity changed")
		}
	}
	for path, identity := range links {
		after, err := agyLinkIdentity(path, filepath.Join(original, strings.TrimPrefix(path, owned+string(filepath.Separator))))
		if err != nil || after != identity {
			t.Error("readonly synthetic reference identity changed")
		}
	}
	if !existing {
		for configName := range config {
			if _, err := os.Lstat(filepath.Join(original, configName)); !errors.Is(err, os.ErrNotExist) {
				t.Error("missing original configuration source was created")
			}
			if configName != "cache/default_project_id.txt" {
				if _, err := os.Lstat(filepath.Join(owned, configName)); !errors.Is(err, os.ErrNotExist) {
					t.Error("missing settings/keybindings were created")
				}
			}
		}
	}
}
