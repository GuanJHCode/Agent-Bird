package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// This fixture only prepares commands and reads/writes its own temp files.
// It never starts the provider, sandbox-exec, git, or another OS actor.
func agyHistoryFixture(t *testing.T) (*Host, process.Command, *adapter.ExecutionProfile, contract.LaunchCommand, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	gemini := filepath.Join(home, ".gemini")
	original := filepath.Join(gemini, "antigravity-cli")
	for _, dir := range []string{home, gemini, original, filepath.Join(original, "cache")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"settings.json":                `{"modelProvider":"original","relativeFixture":"./config"}`,
		"keybindings.json":             `{"original":"binding"}`,
		"cache/default_project_id.txt": "original-project\n",
	} {
		if err = os.WriteFile(filepath.Join(original, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	pin := adapter.BinaryPin{Path: "/pinned/agy", Version: "1.2.16", SHA256: "7dca095cfc1df2c057a385ed88a76c7ba98dc103258a80be87a8f42e484cb3aa"}
	payload := adapter.InvocationPayload{Kind: "provider", Provider: "antigravity-cli", Directory: root, Profile: profile,
		ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: pin}}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	g := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, AdapterPayload: body}
	cmd := process.Command{Path: pin.Path, Dir: root, Args: []string{"--output-format", "stream-json", "--input-format", "stream-json", "--mode", "plan"}, Env: []string{"HOME=" + home, "AUTH_SOURCE_SENTINEL=unchanged"}}
	return h, cmd, profile, g, original
}

func agyHistoryCompletedSourceFixture(t *testing.T, h *Host, g contract.LaunchCommand) contract.LaunchCommand {
	t.Helper()
	if err := h.recordContinuationOrigin(g, nil); err != nil {
		t.Fatal(err)
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	// Pure receipt fixtures model stored facts only. They do not establish a
	// real Wait/tree proof; separate owned-Handle gates verify those call sites.
	identity := &contract.ProcessIdentity{PID: 123, PGID: 123, Birth: "pure-stored-identity"}
	session := "9adeef5e-cd28-4c60-9858-a3462e408814"
	exit := 0
	for i, kind := range []string{contract.EventSession, contract.EventQuestion, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: h.producerID, EventID: kind + "-first", Kind: kind,
			RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision,
			Sequence: int64(i + 1), PayloadHash: hashText(kind), Process: identity}
		if kind == contract.EventExited {
			e.ExitCode = &exit
		} else {
			e.SessionKind, e.SessionID = "conversation-id", session
		}
		if kind == contract.EventQuestion {
			e.QuestionID, e.QuestionRevision, e.QuestionKind = "question", 1, "technical"
		}
		if err = spool.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	g.Continuation = &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host",
		SourceSegmentID: g.SegmentID, SourceEventID: contract.EventQuestion + "-first", SourceEventRevision: 2,
		SourceEventSHA256: hashText(contract.EventQuestion), SessionEventID: contract.EventSession + "-first",
		AdapterPayloadSHA256: hashBytes(g.AdapterPayload), OwnerContextID: "owner"}
	g.SegmentID, g.SessionKind, g.SessionID = "second", "conversation-id", session
	g.QuestionID, g.QuestionRevision, g.Answer = "question", 1, "17"
	if err = h.validateTrustedContinuation(g); err != nil {
		t.Fatalf("source fixture does not satisfy existing origin/Exited guards: %v", err)
	}
	return g
}

func TestAGYHistoryResumeRejectsMissingCompletedSegmentSeal(t *testing.T) {
	h, cmd, profile, g, _ := agyHistoryFixture(t)
	first, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(commandEnvironment(cmd, "HOME"), ".gemini", agyHistoryArgument(t, first))
	if err = os.Mkdir(filepath.Join(owned, "conversations"), 0700); err != nil {
		t.Fatal(err)
	}
	g = agyHistoryCompletedSourceFixture(t, h, g)
	if err = os.WriteFile(filepath.Join(owned, "conversations", g.SessionID+".pb"), []byte("native-state-fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch-second"), g); err == nil {
		t.Fatal("resume admitted .pb without its proved completed-segment seal")
	}
}

func agyHistoryArgument(t *testing.T, cmd process.Command) string {
	t.Helper()
	for i, arg := range cmd.Args {
		if arg == "--app_data_dir" && i+1 < len(cmd.Args) {
			value := cmd.Args[i+1]
			if value == "" || value == "." || value == ".." || filepath.IsAbs(value) || filepath.Base(value) != value || strings.ContainsAny(value, "/\\\x00\r\n") {
				t.Fatalf("unsafe generated relative app-data component: %q", value)
			}
			return value
		}
	}
	t.Fatal("AGY command has no bound owned app-data flag")
	return ""
}

func TestAGYHistoryPreparationRetainsOriginalRootsAndReadonlyReferences(t *testing.T) {
	h, cmd, profile, g, original := agyHistoryFixture(t)
	originalArgs := strings.Join(cmd.Args, "\x00")
	originalEnv := strings.Join(cmd.Env, "\x00")
	wrapped, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
	if err != nil {
		t.Fatal(err)
	}
	component := agyHistoryArgument(t, wrapped)
	owned := filepath.Join(filepath.Dir(original), component)
	if _, err = continuationDirectoryIdentity(owned, 0700); err != nil {
		t.Fatalf("history is not an owned private directory: %v", err)
	}
	if commandEnvironment(wrapped, "HOME") != commandEnvironment(cmd, "HOME") || commandEnvironment(wrapped, "AUTH_SOURCE_SENTINEL") != "unchanged" || wrapped.Dir != cmd.Dir {
		t.Fatal("original cwd, HOME or account environment changed")
	}
	if strings.Join(cmd.Args, "\x00") != originalArgs || strings.Join(cmd.Env, "\x00") != originalEnv {
		t.Fatal("caller command slices mutated")
	}
	if strings.Contains(strings.Join(wrapped.Args, "\x00"), "--gemini_dir") {
		t.Fatal("original Gemini root relocated")
	}
	rules := wrapped.Args[1]
	if !strings.Contains(rules, "(allow file-write* (subpath "+strconv.Quote(owned)+"))") {
		t.Fatal("owned history is not writable")
	}
	for _, name := range []string{"settings.json", "keybindings.json", "cache/default_project_id.txt"} {
		source, link := filepath.Join(original, name), filepath.Join(owned, name)
		target, err := os.Readlink(link)
		if err != nil || target != source {
			t.Fatalf("original per-file config reference %s missing or relocated: %v", name, err)
		}
		for _, protected := range []string{source, link} {
			if !strings.Contains(rules, "(deny file-write* (literal "+strconv.Quote(protected)+"))") {
				t.Fatalf("config source or link is not protected: %s", name)
			}
		}
	}
	for _, global := range []string{commandEnvironment(cmd, "HOME"), filepath.Dir(original), original} {
		if strings.Contains(rules, "(allow file-write* (subpath "+strconv.Quote(global)+"))") {
			t.Fatal("global HOME/Gemini/app-data made writable")
		}
	}
}

func TestAGYHistoryRejectsDanglingOriginalDirectories(t *testing.T) {
	for _, name := range []string{"app", "cache", "settings"} {
		t.Run(name, func(t *testing.T) {
			h, cmd, profile, g, original := agyHistoryFixture(t)
			path := original
			if name == "cache" {
				path = filepath.Join(original, "cache")
			} else if name == "settings" {
				path = filepath.Join(original, "settings.json")
			}
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".missing", path); err != nil {
				t.Fatal(err)
			}
			if _, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g); err == nil {
				t.Fatal("dangling original alias treated as a genuinely absent config source")
			}
		})
	}
}

func TestAGYHistoryConfigAbsenceKeepsDefaultsAndAllowsOnlyMutableIndex(t *testing.T) {
	for _, missing := range []string{"settings", "default", "app"} {
		t.Run(missing, func(t *testing.T) {
			h, cmd, profile, g, original := agyHistoryFixture(t)
			if missing == "app" {
				if err := os.RemoveAll(original); err != nil {
					t.Fatal(err)
				}
			} else {
				name := "settings.json"
				if missing == "default" {
					name = "cache/default_project_id.txt"
				}
				if err := os.Remove(filepath.Join(original, name)); err != nil {
					t.Fatal(err)
				}
			}
			first, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
			if err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, first))
			if missing == "app" || missing == "default" {
				index := filepath.Join(owned, "cache/default_project_id.txt")
				if strings.Contains(first.Args[1], "(deny file-write* (literal "+strconv.Quote(index)+"))") {
					t.Fatal("missing global index forbids owned first creation")
				}
				if err = os.WriteFile(index, []byte("owned-index"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "again"), g); err != nil {
					t.Fatalf("owned first index creation changed original absence binding: %v", err)
				}
			}
			if missing == "app" || missing == "settings" {
				settings := filepath.Join(owned, "settings.json")
				if !strings.Contains(first.Args[1], "(deny file-write* (literal "+strconv.Quote(settings)+"))") {
					t.Fatal("missing settings silently granted provider config writes")
				}
				if err = os.WriteFile(settings, []byte("changed defaults"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "changed"), g); err == nil {
					t.Fatal("created settings changed original default semantics")
				}
			}
		})
	}
}

func TestAGYHistoryPreparationRejectsConfigAndScopeDrift(t *testing.T) {
	for _, fault := range []string{"content", "inode", "link", "global_cache", "owned_root", "metadata", "metadata_mode", "workspace", "home", "app_override", "gemini_override"} {
		t.Run(fault, func(t *testing.T) {
			h, cmd, profile, g, original := agyHistoryFixture(t)
			first, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), g)
			if err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, first))
			settings := filepath.Join(original, "settings.json")
			metadata := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "provider-history.json")
			switch fault {
			case "content":
				err = os.WriteFile(settings, []byte(`{"modelProvider":"different"}`), 0600)
			case "inode":
				body, readErr := os.ReadFile(settings)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.Rename(settings, settings+".saved")
				if err == nil {
					err = os.WriteFile(settings, body, 0600)
				}
			case "link":
				link := filepath.Join(owned, "settings.json")
				err = os.Remove(link)
				if err == nil {
					err = os.Symlink(settings, link)
				}
			case "global_cache":
				err = os.WriteFile(filepath.Join(original, "cache/default_project_id.txt"), []byte("different-project"), 0600)
			case "owned_root":
				err = os.Rename(owned, owned+".saved")
				if err == nil {
					err = os.Mkdir(owned, 0700)
				}
			case "metadata":
				err = os.WriteFile(metadata, []byte(`{"version":1}`), 0600)
			case "metadata_mode":
				err = os.Chmod(metadata, 0644)
			case "workspace":
				cmd.Dir = filepath.Join(cmd.Dir, "other-workspace")
				err = os.Mkdir(cmd.Dir, 0700)
			case "home":
				setCommandEnvironment(&cmd, "HOME", filepath.Join(cmd.Dir, "foreign-home"))
			case "app_override":
				cmd.Args = append(cmd.Args, "--app_data_dir", "../foreign")
			case "gemini_override":
				cmd.Args = append(cmd.Args, "--gemini_dir=/foreign")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "again"), g); err == nil {
				t.Fatal("original immutable config/scope drift was admitted")
			}
		})
	}
}

func TestAGYHistorySealBindsExactCompletedSegmentAndNativeBytes(t *testing.T) {
	for _, fault := range []string{"none", "atomic_same_bytes", "inplace", "missing", "symlink", "hardlink", "permissions", "conversations", "seal_missing", "seal_partial", "foreign_session", "foreign_source", "foreign_owner", "source_hash", "process"} {
		t.Run(fault, func(t *testing.T) {
			h, cmd, profile, first, original := agyHistoryFixture(t)
			prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), first)
			if err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, prepared))
			conversations := filepath.Join(owned, "conversations")
			if err = os.Mkdir(conversations, 0700); err != nil {
				t.Fatal(err)
			}
			resume := agyHistoryCompletedSourceFixture(t, h, first)
			path := filepath.Join(conversations, resume.SessionID+".pb")
			if err = os.WriteFile(path, []byte("native-state"), 0644); err != nil {
				t.Fatal(err)
			}
			identity := &process.Identity{PID: 123, PGID: 123, Birth: "pure-stored-identity"}
			if err = h.captureAGYHistorySeal(first, identity, contract.EventQuestion); err != nil {
				t.Fatal(err)
			}
			sealPath := filepath.Join(h.spoolRoot, first.AttemptID, first.SegmentID, agySealName)
			switch fault {
			case "atomic_same_bytes":
				err = os.Rename(path, path+".saved")
				if err == nil {
					err = os.WriteFile(path, []byte("native-state"), 0644)
				}
			case "inplace":
				err = os.WriteFile(path, []byte("changed-state"), 0644)
			case "missing":
				err = os.Remove(path)
			case "symlink":
				err = os.Rename(path, path+".saved")
				if err == nil {
					err = os.Symlink(path+".saved", path)
				}
			case "hardlink":
				err = os.Link(path, path+".alias")
			case "permissions":
				err = os.Chmod(path, 0666)
			case "conversations":
				err = os.Rename(conversations, conversations+".saved")
				if err == nil {
					err = os.Mkdir(conversations, 0700)
				}
				if err == nil {
					err = os.Rename(filepath.Join(conversations+".saved", resume.SessionID+".pb"), path)
				}
			case "seal_missing":
				err = os.Remove(sealPath)
			case "seal_partial":
				err = os.WriteFile(sealPath, []byte(`{"version":1`), 0600)
			case "foreign_session":
				resume.SessionID = "a901515d-ca21-4282-8e44-34a60943d8c6"
			case "foreign_source":
				resume.Continuation.SourceSegmentID = "foreign"
			case "foreign_owner":
				resume.Continuation.OwnerContextID = "foreign"
			case "source_hash":
				resume.Continuation.SourceEventSHA256 = hashText("foreign")
			case "process":
				var seal agyHistorySeal
				if err = readContinuationMetadata(sealPath, &seal); err != nil {
					t.Fatal(err)
				}
				seal.Process.PID++
				body, marshalErr := json.Marshal(seal)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(sealPath, body, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "second"), resume)
			if fault == "none" && err != nil {
				t.Fatalf("valid source and exact native state refused: %v", err)
			} else if fault != "none" && err == nil {
				t.Fatal("changed/foreign/unsealed native source was admitted")
			}
		})
	}
}

func TestAGYHistoryAtomicSaveUsesNextSegmentsOwnSeal(t *testing.T) {
	h, cmd, profile, first, original := agyHistoryFixture(t)
	prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), first)
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, prepared))
	conversations := filepath.Join(owned, "conversations")
	if err = os.Mkdir(conversations, 0700); err != nil {
		t.Fatal(err)
	}
	second := agyHistoryCompletedSourceFixture(t, h, first)
	path := filepath.Join(conversations, second.SessionID+".pb")
	if err = os.WriteFile(path, []byte("first-native-state"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = h.captureAGYHistorySeal(first, &process.Identity{PID: 123, PGID: 123, Birth: "pure-stored-identity"}, contract.EventQuestion); err != nil {
		t.Fatal(err)
	}
	firstSealPath := filepath.Join(h.spoolRoot, first.AttemptID, first.SegmentID, agySealName)
	firstSealBytes, err := os.ReadFile(firstSealPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "second"), second); err != nil {
		t.Fatal(err)
	}
	// Model the verified native Save algorithm: a different inode and complete
	// next-segment state. It cannot rewrite the previous protected receipt.
	if err = os.WriteFile(path+".native.tmp", []byte("second-native-state"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".native.tmp", path); err != nil {
		t.Fatal(err)
	}
	spool, err := h.spool(store.Attempt{ID: second.AttemptID, SegmentID: second.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	processID := &contract.ProcessIdentity{PID: 124, PGID: 124, Birth: "pure-second-identity"}
	exit := 0
	for i, kind := range []string{contract.EventSession, contract.EventQuestion, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: h.producerID, EventID: kind + "-second", Kind: kind, RunID: first.RunID, TaskID: first.TaskID, AttemptID: first.AttemptID, SegmentID: second.SegmentID, WorkRevision: 1, Sequence: int64(i + 4), PayloadHash: hashText(kind + "-second"), Process: processID}
		if kind == contract.EventExited {
			e.ExitCode = &exit
		} else {
			e.SessionKind, e.SessionID = second.SessionKind, second.SessionID
		}
		if kind == contract.EventQuestion {
			e.QuestionID, e.QuestionRevision, e.QuestionKind = "question-second", 2, "technical"
		}
		if err = spool.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.captureAGYHistorySeal(second, &process.Identity{PID: 124, PGID: 124, Birth: "pure-second-identity"}, contract.EventQuestion); err != nil {
		t.Fatal(err)
	}
	third := second
	copyRef := *second.Continuation
	third.Continuation = &copyRef
	third.SegmentID, third.QuestionID, third.QuestionRevision = "third", "question-second", 2
	third.Continuation.SourceSegmentID, third.Continuation.SourceEventID = second.SegmentID, contract.EventQuestion+"-second"
	third.Continuation.SourceEventRevision, third.Continuation.SourceEventSHA256 = 5, hashText(contract.EventQuestion+"-second")
	if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "third"), third); err != nil {
		t.Fatalf("legitimate atomic Save incorrectly tied to first inode: %v", err)
	}
	if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "stale"), second); err == nil {
		t.Fatal("previous segment receipt accepted the next segment's changed native state")
	}
	current, err := os.ReadFile(firstSealPath)
	if err != nil || string(current) != string(firstSealBytes) {
		t.Fatal("first immutable receipt was rewritten")
	}
}

func TestAGYHistorySealNeverOverwritesCompletedOrPartialReceipt(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err = continuationOwnedDirectory(dir, 0700); err != nil {
				t.Fatalf("seal fixture must begin with a private canonical directory: %v", err)
			}
			first := agyHistorySeal{Version: 1, SegmentID: "first", SHA256: hashText("first")}
			if partial {
				if err = os.WriteFile(filepath.Join(dir, "."+agySealName+".tmp"), []byte(`{"version":`), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err = writeAGYHistorySeal(dir, first); err != nil {
				t.Fatal(err)
			}
			second := first
			second.SHA256 = hashText("different")
			if err = writeAGYHistorySeal(dir, second); err == nil {
				t.Fatal("existing/partial receipt silently overwritten")
			}
			var actual agyHistorySeal
			err = readContinuationMetadata(filepath.Join(dir, agySealName), &actual)
			if partial && err == nil {
				t.Fatal("partial stage appeared as a successful receipt")
			} else if !partial && (err != nil || actual != first) {
				t.Fatal("original completed receipt changed")
			}
		})
	}
}
