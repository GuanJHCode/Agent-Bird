package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func configReceiverPackage(t *testing.T) (string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		".codex-plugin/plugin.json":        {`{"name":"codex-orchestrator","version":"1.0.0"}`, 0600},
		"defaults.json":                    {`{"version":1,"write_workspace":"isolated-worktree","allow_shared_write":false,"provider_scope":"codex-session","model":"cli-default"}`, 0600},
		"bin/codex-orchestrator":           {"synthetic immutable package binary", 0700},
		"scripts/invoke.sh":                {"synthetic entry", 0700},
		"skills/orchestrate/SKILL.md":      {"synthetic skill", 0600},
		"scripts/agent-bird-hook":          {"synthetic hook", 0700},
		"scripts/agent-bird-agy-hook":      {"synthetic AGY hook", 0700},
		"hooks/agy-proposed-hooks.json":    {"synthetic proposed hooks", 0600},
		"runtime/g0/runtime-manifest.json": {`{"version":2,"kind":"collect-only"}`, 0600},
	}
	var metadata []map[string]any
	for path, f := range files {
		full := filepath.Join(source, path)
		if err = os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(full, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(f.body))
		metadata = append(metadata, map[string]any{"path": path, "sha256": hex.EncodeToString(h[:]), "mode": uint32(f.mode)})
	}
	body, _ := json.Marshal(map[string]any{"schema_version": 1, "kind": "collect-only-hooks", "files": metadata})
	if err = os.WriteFile(filepath.Join(source, "payload-manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "installed")
	state := filepath.Join(root, "state")
	if _, err = install.InstallPackage(install.InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: state, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return dest, filepath.Join(dest, "versions/1.0.0/bin/codex-orchestrator"), state
}

func TestConfigGenerationControlAuthenticatesBeforePackageAndSubmit(t *testing.T) {
	_, nominated, state := configReceiverPackage(t)
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{db: db, stateDir: state}
	// The ancestry is trusted input to control, not a test of authorizePeer's
	// kernel/UDS inspection. No Server/Host or process is started here.
	ctx := context.WithValue(context.Background(), peerAncestryKey{}, map[int]string{111: "birth"})
	bound, err := s.bindLocalOwner(ctx, mustConfigJSON(t, OwnerBindRequest{ControllerThread: "controller", OriginPID: 111, OriginBirth: "birth"}))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		OwnerCapability string `json:"owner_capability"`
	}
	if err = json.Unmarshal(bound, &receipt); err != nil {
		t.Fatal(err)
	}
	owner, err := s.localOwner(ctx, receipt.OwnerCapability)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	ref := &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: scope, GenerationID: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64), PackageVersion: "1.0.0"}
	payload := adapter.InvocationPayload{Kind: "fake", Provider: string(adapter.ProviderAGY), ConfigGeneration: ref, ConfigModelSource: "not-applicable"}
	request := SubmitRequest{OwnerMode: "local", OwnerCapability: receipt.OwnerCapability, ControllerThread: owner.ControllerThread, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration, RunID: "run", PlanRevision: 1, HostExecutable: nominated, Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1, AdapterPayload: mustConfigJSON(t, payload)}}}
	for _, name := range []string{"invalid-owner-before-package", "tuple-before-package", "nominated-package-before-store", "snapshot-owner-before-package"} {
		t.Run(name, func(t *testing.T) {
			changed := request
			want := "config_package_mismatch"
			kind := ipc.KindSubmit
			var raw json.RawMessage
			switch name {
			case "invalid-owner-before-package":
				changed.OwnerCapability = filepath.Join(state, "owners", "missing.json")
				want = "owner_capability_invalid"
			case "tuple-before-package":
				changed.ControllerThread = "foreign"
				want = "owner_capability_mismatch"
			case "snapshot-owner-before-package":
				kind = ipc.KindConfigGeneration
				want = "owner_capability_invalid"
				raw = mustConfigJSON(t, ConfigGenerationRequest{OwnerCapability: filepath.Join(state, "owners", "missing.json"), Action: "status"})
			}
			if raw == nil {
				raw = mustConfigJSON(t, changed)
			}
			if _, err := s.control(ctx, kind, raw); err == nil || err.Error() != want {
				t.Fatalf("want %s before any Submit, got %v", want, err)
			}
			if _, err := db.TaskSnapshot(ctx, "task"); err == nil {
				t.Fatal("denied request reached Store.SubmitPlan")
			}
		})
	}
}

func mustConfigJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConfigGenerationOperationRejectsNonClosedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"request_id":"off","action":"deactivate","foreign":true}`,
		`{"request_id":"off","action":"deactivate","action":"deactivate"}`,
		`{"request_id":"off","Action":"deactivate"}`,
		`{"request_id":"off","action":"deactivate","expected":null}`,
		`{"request_id":"a","action":"publish","saved_defaults":{"antigravity-cli":null,"claude-code":"","codex-cli":""}}`,
	} {
		if _, err := DecodeConfigGenerationOperation([]byte(raw)); err == nil {
			t.Fatalf("non-closed operation admitted: %s", raw)
		}
	}
	if _, err := DecodeConfigGenerationOperation([]byte(`{"request_id":"off","action":"deactivate"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigGenerationReceiverKeepsLegacyEmptyAdapter(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{db: db, stateDir: state}
	ctx := context.Background()
	raw := mustConfigJSON(t, SubmitRequest{RunID: "legacy", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 111, OriginBirth: "birth", HostGeneration: "generation", HostExecutable: "/synthetic/legacy", Tasks: []TaskRequest{{ID: "legacy-task", MaxAttempts: 1}}})
	// Existing callers may omit adapter entirely, rather than encode null.
	raw = json.RawMessage(strings.Replace(string(raw), `,"adapter":null`, "", 1))
	if strings.Contains(string(raw), `"adapter":`) {
		t.Fatal("fixture did not omit adapter")
	}
	if _, err := s.control(ctx, ipc.KindSubmit, raw); err != nil {
		t.Fatal("new pair detection changed original empty legacy adapter admission", err)
	}
	if _, err := db.TaskSnapshot(ctx, "legacy-task"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigGenerationReceiverUsesAuthenticatedPackageAndOriginalPin(t *testing.T) {
	_, exe, state := configReceiverPackage(t)
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	g, e := db.CreateOwnerGrant(context.Background(), "controller", 111, "birth")
	if e != nil {
		t.Fatal(e)
	}
	a, e := db.ConfigPreparationAuthority(context.Background(), g, nil)
	if e != nil {
		t.Fatal(e)
	}
	owner := store.OwnerGrant{ControllerThread: "controller", OriginBirth: "birth"}
	scope, _ := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "claude-a", Codex: ""}
	published, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults})
	if err != nil {
		t.Fatal(err)
	}
	payload := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderAGY), Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, Model: "a"}, ConfigGeneration: published.Ref, ConfigModelSource: "saved-default"}
	raw, _ := json.Marshal(payload)
	request := SubmitRequest{OwnerMode: "local", RunID: "run", PlanRevision: 1, HostExecutable: exe, Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: raw}}}
	use, err := install.BeginConfigPreparation(exe, scope, "run", []string{"task"}, strings.Repeat("1", 64), &a)
	if err != nil {
		t.Fatal(err)
	}
	if err = install.PinConfigPreparation(exe, use, ConfigTaskPlanDigest(request.RunID, request.PlanRevision, request.Tasks)); err != nil {
		t.Fatal(err)
	}
	defaults.AGY = "b"
	if _, err = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: published.Active, SavedDefaults: &defaults}); err != nil {
		t.Fatal(err)
	}
	if err = validateConfigSubmission(request, exe, owner); err != nil {
		t.Fatal("old A must not consult current B", err)
	}
	for _, name := range []string{"foreign-owner", "nominated-executable", "foreign-actual-package", "drifted-plan", "drifted-default", "missing-fallback-ref"} {
		t.Run(name, func(t *testing.T) {
			changed := request
			changed.Tasks = append([]TaskRequest(nil), request.Tasks...)
			auth := owner
			actual := exe
			switch name {
			case "foreign-owner":
				auth.OriginBirth = "other"
			case "nominated-executable":
				changed.HostExecutable = filepath.Join(filepath.Dir(exe), "foreign")
			case "foreign-actual-package":
				_, actual, _ = configReceiverPackage(t)
				changed.HostExecutable = actual
			case "drifted-plan":
				changed.Tasks[0].MaxActiveMS++
			case "drifted-default":
				changed.Tasks[0].AdapterPayload = []byte(strings.Replace(string(raw), `"model":"a"`, `"model":"b"`, 1))
			case "missing-fallback-ref":
				changed.Tasks[0].Fallbacks = []json.RawMessage{json.RawMessage(`{"kind":"fake"}`)}
			}
			if err := validateConfigSubmission(changed, actual, auth); err == nil {
				t.Error("new consumer admitted mismatched authenticated package/original use")
			}
		})
	}
	// Both pair fields absent keeps the original legacy Submit compatibility.
	request.Tasks[0].AdapterPayload = json.RawMessage(`{"kind":"gitops"}`)
	request.HostExecutable = "original-legacy-path"
	if err = validateConfigSubmission(request, "uninstalled-legacy", owner); err != nil {
		t.Fatal("legacy consumer compatibility changed", err)
	}
	// The owner tuple above is a trusted-input predicate fixture, not kernel proof.
}

func TestConfigPreparationAuthorityIPCUsesActualStore(t *testing.T) {
	_, _, state := configReceiverPackage(t)
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &Server{db: db, stateDir: state}
	ctx := context.WithValue(context.Background(), peerAncestryKey{}, map[int]string{111: "birth"})
	bind := func() string {
		raw, e := s.bindLocalOwner(ctx, mustConfigJSON(t, OwnerBindRequest{ControllerThread: "controller", OriginPID: 111, OriginBirth: "birth"}))
		if e != nil {
			t.Fatal(e)
		}
		var b struct {
			OwnerCapability string `json:"owner_capability"`
		}
		if json.Unmarshal(raw, &b) != nil {
			t.Fatal("owner bind receipt")
		}
		return b.OwnerCapability
	}
	first := bind()
	response, e := s.control(ctx, ipc.KindConfigGeneration, mustConfigJSON(t, ConfigGenerationRequest{OwnerCapability: first, Action: "preparation-authority"}))
	if e != nil {
		t.Fatal(e)
	}
	var a contract.ConfigPreparationAuthority
	if json.Unmarshal(response, &a) != nil {
		t.Fatal("closed authority response")
	}
	second := bind()
	response, e = s.control(ctx, ipc.KindConfigGeneration, mustConfigJSON(t, ConfigGenerationRequest{OwnerCapability: second, Action: "preparation-authority", OriginalAuthority: &a}))
	var replay contract.ConfigPreparationAuthority
	if e != nil || json.Unmarshal(response, &replay) != nil || replay != a {
		t.Fatal("same-owner new grant changed original authority", e)
	}
	_, _, foreignState := configReceiverPackage(t)
	foreign, e := store.Open(filepath.Join(foreignState, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer foreign.Close()
	other := &Server{db: foreign, stateDir: foreignState}
	raw, e := other.bindLocalOwner(ctx, mustConfigJSON(t, OwnerBindRequest{ControllerThread: "controller", OriginPID: 111, OriginBirth: "birth"}))
	if e != nil {
		t.Fatal(e)
	}
	var b struct {
		OwnerCapability string `json:"owner_capability"`
	}
	json.Unmarshal(raw, &b)
	if _, e = other.control(ctx, ipc.KindConfigGeneration, mustConfigJSON(t, ConfigGenerationRequest{OwnerCapability: b.OwnerCapability, Action: "preparation-authority", OriginalAuthority: &a})); e == nil || e.Error() != "config_preparation_store_mismatch" {
		t.Fatal("caller original ref established foreign Store authority", e)
	}
	// Auth is checked before this nonsecret original Store reference is consulted.
	if _, e = s.control(ctx, ipc.KindConfigGeneration, mustConfigJSON(t, ConfigGenerationRequest{OwnerCapability: filepath.Join(state, "owners", "missing.json"), Action: "preparation-authority", OriginalAuthority: &a})); e == nil || e.Error() != "owner_capability_invalid" {
		t.Fatal("authority action skipped localOwner", e)
	}
}
