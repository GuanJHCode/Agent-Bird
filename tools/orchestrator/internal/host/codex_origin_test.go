package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func codexOriginFixture(t *testing.T) (*Host, contract.LaunchCommand, adapter.Invocation, []string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	cwd := filepath.Join(base, "workspace")
	spoolRoot := filepath.Join(base, "spool")
	for _, path := range []string{source, cwd, spoolRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte("model=\"gpt-fixed\"\nmodel_reasoning_effort=\"source-effort\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("SYNTHETIC_AUTH_NEVER_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(spoolRoot, "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	binary := adapter.BinaryPin{Path: "/owned/pinned/codex", Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: binary}
	req := adapter.Request{Provider: adapter.ProviderCodex, Binary: binary, CWD: cwd, Prompt: "readonly task", Profile: &profile, Lock: &lock}
	inv, err := adapter.BuildManagedCodexInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	p := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: cwd, Prompt: req.Prompt, Profile: &profile, ProviderLock: &lock}
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "initial", WorkRevision: 1, PlanRevision: 1, CommandID: "command", HostID: "producer", ExecutionEpoch: 1, AdapterPayload: payload})
	// This is the real spool API's safe owned preparation, not a process proof.
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal(err)
	}
	return h, g, inv, []string{"HOME=" + base, "CODEX_HOME=" + source}
}

func TestCodexOriginPreparationUsesOriginalDefaultsAndExclusiveProtectedState(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	prepared, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	o := prepared.Origin
	if o.Version != 1 || o.Scope.InitialSegmentID != g.SegmentID || o.Scope.OwnerContextID != "owner" || o.Backend != adapter.ManagedCodexBackend || !codexCanonicalUUID.MatchString(o.InstallationID) || o.InitialAuxiliary.Entries != 0 || len(o.Parents) > 32 || !strings.Contains(string(prepared.Projection.ConfigTOML()), "source-effort") {
		t.Fatal("origin/default/layout scope mismatch")
	}
	var read codexOriginRecord
	file, err := codexReadProtectedRecord(context.Background(), prepared.Protected, "codex-origin.json", &read)
	if err != nil || file != prepared.OriginFile || read.Scope != o.Scope {
		t.Fatal("exclusive original record not durable", err)
	}
	raw, err := os.ReadFile(filepath.Join(prepared.Protected.Path, "codex-origin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 16<<10 || strings.Contains(string(raw), "SYNTHETIC_AUTH") || strings.Contains(string(raw), "source-effort") {
		t.Fatal("origin overflow or source/auth scalar content export")
	}
	link, err := os.Readlink(filepath.Join(prepared.Layout.Home.Path, "auth.json"))
	if err != nil || link != filepath.Join(o.Source.Home, "auth.json") {
		t.Fatal("account reference changed")
	}
	if _, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{}); err == nil {
		t.Fatal("same request adopted existing origin/state instead of refusing duplicate")
	}
	preserved, err := os.ReadFile(filepath.Join(prepared.Protected.Path, "codex-origin.json"))
	if err != nil || codexDigest(preserved) != file.SHA256 {
		t.Fatal("duplicate preparation overwrote evidence")
	}
}

func TestCodexOriginPreparationFinalBarrierRejectsEarlierDrift(t *testing.T) {
	for _, kind := range []string{"source_config", "source_auth", "owned_config", "auth_link", "forbidden_cache", "absent_wal", "workspace", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			state := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state")
			_, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{BeforePublish: func() {
				reached = true
				source := strings.TrimPrefix(env[1], "CODEX_HOME=")
				var path string
				body := []byte("replacement")
				switch kind {
				case "source_config":
					path = filepath.Join(source, "config.toml")
					body = []byte("model=\"gpt-fixed\"\nmodel_reasoning_effort=\"source-effort\"\n")
				case "source_auth":
					path = filepath.Join(source, "auth.json")
					body = []byte("SYNTHETIC_AUTH_NEVER_READ")
				case "owned_config":
					path = filepath.Join(state, "home", "config.toml")
				case "auth_link":
					path = filepath.Join(state, "home", "auth.json")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(source, "auth.json"), path); err != nil {
						t.Fatal(err)
					}
					return
				case "forbidden_cache":
					path = filepath.Join(state, "home", "models_cache.json")
				case "absent_wal":
					path = filepath.Join(state, "sqlite", "state_5.sqlite-wal")
				case "workspace":
					path = inv.WorkingDirectory()
					if err := os.Rename(path, path+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					return
				case "cancel":
					cancel()
					return
				}
				tmp := filepath.Join(filepath.Dir(path), "replacement")
				if err := os.WriteFile(tmp, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}})
			if !reached || !errors.Is(err, errCodexStateFile) {
				t.Fatal("actual original preparation checkpoint not rejected", reached, err)
			}
			if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-origin.json")); !os.IsNotExist(err) {
				t.Fatal("failed final capture published an origin")
			}
			if _, err := os.Lstat(state); err != nil {
				t.Fatal("failed state evidence erased")
			}
		})
	}
}

func TestCodexOriginPreparationRejectsInvocationPayloadAndForeignState(t *testing.T) {
	for _, kind := range []string{"owner", "scope", "payload", "legacy", "source_env_duplicate", "source_overlap", "foreign_state", "unsafe_spool_parent"} {
		t.Run(kind, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			switch kind {
			case "owner":
				h.continuationOwner = ""
			case "scope":
				g.AttemptID = "../escape"
			case "payload":
				var p adapter.InvocationPayload
				if json.Unmarshal(g.AdapterPayload, &p) != nil {
					t.Fatal("fixture")
				}
				p.Prompt = "different"
				g.AdapterPayload, _ = json.Marshal(p)
			case "legacy":
				p := inv.Pin()
				var err error
				inv, err = adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p, CWD: inv.WorkingDirectory(), Prompt: "legacy", Permission: adapter.Permission{Mode: "plan"}})
				if err != nil {
					t.Fatal(err)
				}
			case "source_env_duplicate":
				env = append(env, env[1])
			case "source_overlap":
				env[1] = "CODEX_HOME=" + inv.WorkingDirectory()
			case "foreign_state":
				if err := os.Mkdir(filepath.Join(h.spoolRoot, g.AttemptID, "codex-state"), 0700); err != nil {
					t.Fatal(err)
				}
			case "unsafe_spool_parent":
				attempt := filepath.Join(h.spoolRoot, g.AttemptID)
				if err := os.Rename(attempt, attempt+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(attempt+"-original", attempt); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{}); err == nil {
				t.Fatal("unsafe or unbound origin prepared")
			}
		})
	}
}

func TestCodexProtectedOriginVerifiesOriginalParentMapBeforeStart(t *testing.T) {
	for _, kind := range []string{"unchanged", "source_same_bytes", "origin_same_bytes", "runtime_root", "source_root", "workspace_root", "forbidden_cache", "new_auxiliary", "installation_rewrite", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			prepared, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var path string
			switch kind {
			case "source_same_bytes":
				path = filepath.Join(prepared.Source.Home, "config.toml")
			case "origin_same_bytes":
				path = filepath.Join(prepared.Protected.Path, "codex-origin.json")
			case "runtime_root":
				path = prepared.Layout.Home.Path
			case "source_root":
				path = prepared.Source.Home
			case "workspace_root":
				path = inv.WorkingDirectory()
			case "forbidden_cache":
				if err := os.WriteFile(filepath.Join(prepared.Layout.Home.Path, "session_index.jsonl"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "new_auxiliary":
				if err := os.WriteFile(filepath.Join(prepared.Layout.Auxiliary.Runtime[0].Path, "extra"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "installation_rewrite":
				if err := os.Chmod(filepath.Join(prepared.Layout.Home.Path, "installation_id"), 0644); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			if kind == "source_same_bytes" || kind == "origin_same_bytes" {
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				} // Synthetic owned config/control data only; never source auth.
				tmp := filepath.Join(filepath.Dir(path), "replacement")
				if err := os.WriteFile(tmp, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			} else if kind == "runtime_root" || kind == "source_root" || kind == "workspace_root" {
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				// Moving original children cannot substitute for the anchored parent inode.
				children, err := os.ReadDir(path + "-original")
				if err != nil {
					t.Fatal(err)
				}
				for _, child := range children {
					if err := os.Rename(filepath.Join(path+"-original", child.Name()), filepath.Join(path, child.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = codexVerifyInitialPrepared(ctx, prepared)
			if kind == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("changed/unsafe original state admitted before Start")
			}
		})
	}
}

func TestCodexOriginRecordShapeAndPolicyDoNotAdoptMalformedReferences(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	prepared, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing_parent", "duplicate_parent", "extra_parent", "path_drift", "source_hash", "auth_target", "profile", "backend", "required_absence", "installation", "absent_config_hash"} {
		t.Run(kind, func(t *testing.T) {
			o := prepared.Origin
			o.Parents = append([]codexStateDirectory(nil), o.Parents...)
			switch kind {
			case "missing_parent":
				o.Parents = o.Parents[1:]
			case "duplicate_parent":
				o.Parents = append(o.Parents, o.Parents[0])
			case "extra_parent":
				o.Parents = append(o.Parents, codexStateDirectory{Path: "/zzz-unused", Inode: 99, Mode: 0700})
			case "path_drift":
				o.StatePath = filepath.Join(filepath.Dir(o.StatePath), "another-state")
			case "source_hash":
				o.Source.ProjectedSHA256 = strings.Repeat("0", 64)
			case "auth_target":
				o.Owned.AuthTarget = filepath.Join(o.Source.Home, "config.toml")
			case "profile":
				o.Profile.Model = "selected"
			case "backend":
				o.Backend = "legacy-exec"
			case "required_absence":
				o.RequiredAbsences = []string{"models_cache.json"}
			case "installation":
				o.InstallationID = "different"
			case "absent_config_hash":
				o.Source.Config.Present = false
			}
			if _, _, _, err := codexLayoutFromOrigin(context.Background(), o); err == nil {
				t.Fatal("malformed reference/layout adopted")
			}
		})
	}
}

func TestCodexOriginRequiresBoundLayoutProjection(t *testing.T) {
	for _, kind := range []string{"actual_layout", "unbound", "different_layout"} {
		t.Run(kind, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "actual_layout" {
				sqlite, log, bound := p.Projection.RuntimePaths()
				if !bound || sqlite != p.Layout.SQLite.Path || log != p.Layout.Auxiliary.Runtime[3].Path {
					t.Fatal("managed preparation did not bind anchored Layout")
				}
				return
			}
			other, err := codexconfig.Project(p.Projection.ConfigTOML(), codexconfig.ReadOnly)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "different_layout" {
				other, err = other.BindRuntimePaths("/different/sqlite", "/different/log")
				if err != nil {
					t.Fatal(err)
				}
			}
			p.Projection = other
			if codexVerifyInitialPrepared(context.Background(), p) == nil {
				t.Fatal("private managed pre-Start accepted unbound/foreign projection")
			}
		})
	}
}
