package store

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
)

// New indexed plans use real package/generation/preparation/Store admission,
// never SQL-seeded configuration history. Provider execution is a Store fixture.
func bindIndexedConfigPlan(t *testing.T, db *DB, p PlanSpec) PlanSpec {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "installed")
	files := map[string]string{
		".codex-plugin/plugin.json": `{"name":"codex-orchestrator","version":"1.0.0"}`,
		"defaults.json":             `{"version":1,"write_workspace":"isolated-worktree","allow_shared_write":false,"provider_scope":"codex-session","model":"cli-default"}`,
		"bin/codex-orchestrator":    "synthetic immutable Store fixture",
		"scripts/invoke.sh":         "synthetic entry", "skills/orchestrate/SKILL.md": "synthetic skill",
		"scripts/agent-bird-hook": "synthetic hook", "scripts/agent-bird-agy-hook": "synthetic AGY hook",
		"hooks/agy-proposed-hooks.json": "synthetic proposed hooks", "runtime/g0/runtime-manifest.json": `{"version":2,"kind":"collect-only"}`,
	}
	var metadata []map[string]any
	for name, body := range files {
		path := filepath.Join(source, name)
		mode := os.FileMode(0600)
		if strings.HasPrefix(name, "scripts/") || strings.HasPrefix(name, "bin/") {
			mode = 0700
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		metadata = append(metadata, map[string]any{"path": name, "sha256": hex.EncodeToString(sum[:]), "mode": uint32(mode)})
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "kind": "collect-only-hooks", "files": metadata})
	if err = os.WriteFile(filepath.Join(source, "payload-manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = install.InstallPackage(install.InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: filepath.Dir(db.path), Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dest, "versions/1.0.0/bin/codex-orchestrator")
	p.Run.OriginPID = 111 // Trusted Store identity fixture; PID 1 cannot own a preparation.
	g, err := db.CreateOwnerGrant(ctx, p.Run.ControllerThread, p.Run.OriginPID, p.Run.OriginBirth)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := db.ConfigPreparationAuthority(ctx, g, nil)
	if err != nil {
		t.Fatal("Store preparation authority", err)
	}
	scope, err := contract.ConfigGenerationOwnerScope(g.ControllerThread, g.OriginBirth)
	if err != nil {
		t.Fatal(err)
	}
	defaults := install.ConfigSavedDefaults{}
	published, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "indexed-config", Action: "publish", SavedDefaults: &defaults})
	if err != nil {
		t.Fatal(err)
	}
	p.Run.OriginContextID, p.Host.OriginContextID, p.Host.HostGeneration, p.Host.Executable = g.ID, g.ID, g.ID, exe
	ids := make([]string, 0, len(p.Tasks))
	for i := range p.Tasks {
		payload, err := adapter.DecodeInvocationPayload(p.Tasks[i].AdapterPayload)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tasks[i].ID == "edit" {
			payload.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite}
		}
		payload.ConfigGeneration, payload.ConfigModelSource = published.Ref, "not-applicable"
		if payload.Profile != nil {
			payload.Profile.CLIModelDefault = true
			payload.ConfigModelSource = "cli-default"
		}
		p.Tasks[i].AdapterPayload, _ = json.Marshal(payload)
		ids = append(ids, p.Tasks[i].ID)
	}
	uses, paired, err := configPlanTasks(p)
	if err != nil || !paired {
		t.Fatal("config uses", err)
	}
	digest := install.ConfigSubmissionPlanDigest(p.Run.ID, uses)
	use, err := install.BeginConfigPreparation(exe, scope, p.Run.ID, ids, strings.Repeat("1", 64), &authority)
	if err != nil {
		t.Fatal(err)
	}
	if err = install.PinConfigPreparation(exe, use, digest); err != nil {
		t.Fatal(err)
	}
	p.ConfigSubmission, err = install.OpenConfigSubmissionGuard(exe, scope, p.Run.ID, digest, uses)
	if err != nil {
		t.Fatal("submission guard", err)
	}
	return p
}
