package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	c04agy "github.com/GuanJHCode/Agent-Bird/tools/orchestrator/cmd/orchestrator/testdata/c04agy/fixture"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const c04OriginalAGYPin = "7dca095cfc1df2c057a385ed88a76c7ba98dc103258a80be87a8f42e484cb3aa"

// Only the independent fixture binary may establish the test variant's pin.
// Replace one parsed top-level const value, never another byte or source file.
func c04AGYPinOverlay(original []byte, digest string) ([]byte, error) {
	invalid := errors.New("c04_fixture_overlay_invalid")
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return nil, invalid
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "agy_history.go", original, 0)
	if err != nil {
		return nil, invalid
	}
	var value *ast.BasicLit
	count := 0
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range group.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range v.Names {
				if name.Name != "agyHistorySHA256" {
					continue
				}
				count++
				if group.Tok != token.CONST || len(v.Names) != 1 || len(v.Values) != 1 {
					return nil, invalid
				}
				value, ok = v.Values[0].(*ast.BasicLit)
				if !ok || value.Kind != token.STRING || value.Value != strconv.Quote(c04OriginalAGYPin) {
					return nil, invalid
				}
			}
		}
	}
	if count != 1 {
		return nil, invalid
	}
	start, end := fset.Position(value.Pos()).Offset, fset.Position(value.End()).Offset
	replaced := append([]byte(nil), original[:start]...)
	replaced = append(replaced, strconv.Quote(digest)...)
	replaced = append(replaced, original[end:]...)
	return replaced, nil
}

func c04BuildBounded(t *testing.T, ctx context.Context, output string, options ...string) {
	t.Helper()
	deadline, bounded := ctx.Deadline()
	commandDeadline := deadline.Add(-time.Second)
	if !bounded || !time.Now().Before(commandDeadline) {
		t.Fatal("original 60s build preparation window exhausted")
	}
	// Both builds use one preparation deadline. Pipe confirmation gets the
	// last second inside that window, never a new second after its expiry.
	commandCtx, cancel := context.WithDeadline(ctx, commandDeadline)
	defer cancel()
	argv := []string{"build", "-mod=readonly", "-p", "1", "-o", output}
	argv = append(argv, options...)
	cmd := exec.CommandContext(commandCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), argv...)
	cmd.WaitDelay = time.Second
	started := time.Now()
	body, err := cmd.CombinedOutput() // owns the original Go process and Wait
	if err == nil {
		err = ctx.Err()
	}
	rc, pid := -1, 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	record, _ := json.Marshal(map[string]any{"argv": cmd.Args, "original_pid": pid, "original_wait_rc": rc, "elapsed_ms": time.Since(started).Milliseconds(), "deadline_unix_ms": deadline.UnixMilli(), "command_deadline_unix_ms": commandDeadline.UnixMilli(), "wait_delay_ms": cmd.WaitDelay.Milliseconds(), "output_size": len(body), "output_sha256": hex.EncodeToString(c04DigestBytes(body)), "race_option": false})
	t.Logf("C04_FIXTURE_BUILD %s", record)
	if err != nil {
		if len(body) > 64*1024 {
			body = body[:64*1024]
		}
		t.Fatalf("bounded fixture build: %v %s", err, body)
	}
}

func c04DigestBytes(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

func c04BuildExecutables(t *testing.T, root string, sqliteAGY bool) (bin, native string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bin = filepath.Join(root, "orchestrator")
	if !sqliteAGY {
		c04BuildBounded(t, ctx, bin, ".")
		var err error
		native, err = filepath.EvalSymlinks(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err = ctx.Err(); err != nil {
			t.Fatal("original build preparation deadline expired", err)
		}
		return bin, native
	}
	native = filepath.Join(root, "c04-agy")
	c04BuildBounded(t, ctx, native, "./testdata/c04agy")
	fixtureBytes, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSHA := hex.EncodeToString(c04DigestBytes(fixtureBytes))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Clean(filepath.Join(cwd, "..", "..", "internal", "host", "agy_history.go"))
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := c04AGYPinOverlay(before, fixtureSHA)
	if err != nil {
		t.Fatal(err)
	}
	replaced := filepath.Join(root, "agy-history-overlay.go")
	overlay := filepath.Join(root, "overlay.json")
	if err = os.WriteFile(replaced, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeExclusiveJSON(overlay, map[string]any{"Replace": map[string]string{original: replaced}}); err != nil {
		t.Fatal(err)
	}
	overlayBytes, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	c04BuildBounded(t, ctx, bin, "-overlay", overlay, ".")
	after, err := os.ReadFile(original)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("test overlay changed the original production file", err)
	}
	compiled, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctx.Err(); err != nil {
		t.Fatal("original build preparation deadline expired", err)
	}
	record, _ := json.Marshal(map[string]any{"classification": "test-only compiled pin variant; not native AGY proof", "original_path": original, "original_sha256": hex.EncodeToString(c04DigestBytes(before)), "overlay_source_sha256": hex.EncodeToString(c04DigestBytes(replacement)), "overlay_json_sha256": hex.EncodeToString(c04DigestBytes(overlayBytes)), "overlay_mapping": map[string]string{original: replaced}, "fixture_path": native, "fixture_sha256": fixtureSHA, "orchestrator_sha256": hex.EncodeToString(c04DigestBytes(compiled)), "single_const": "agyHistorySHA256"})
	t.Logf("C04_FIXTURE_VARIANT %s", record)
	return bin, native
}

func c04SyntheticAGYHome(t *testing.T, root string) string {
	t.Helper()
	home := filepath.Join(root, "home")
	for _, path := range []string{home, filepath.Join(home, ".gemini"), filepath.Join(home, ".gemini", "antigravity-cli"), filepath.Join(home, ".gemini", "antigravity-cli", "cache")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"settings.json": "{}", "keybindings.json": "{}", "cache/default_project_id.txt": "fixture-project\n"} {
		if err := os.WriteFile(filepath.Join(home, ".gemini", "antigravity-cli", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func c04SQLiteUnitScope(t *testing.T) (c04agy.Config, []string, string) {
	t.Helper()
	root := privateTaskTemp(t)
	home := c04SyntheticAGYHome(t, root)
	component := "agent-bird-" + strings.Repeat("a", 48)
	if err := os.Mkdir(filepath.Join(home, ".gemini", component), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := c04agy.Config{Home: home, CWD: root, Role: "reviewer", Mode: "question"}
	args := []string{"--app_data_dir", component, "--json-schema", adapter.ManagedTaskSchema()}
	return cfg, args, filepath.Join(home, ".gemini", component, "conversations", c04agy.SessionID+".db")
}

func c04SQLiteUnitRun(t *testing.T, cfg c04agy.Config, args []string, input string) ([]byte, bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out bytes.Buffer
	stop, err := c04agy.Run(ctx, cfg, args, strings.NewReader("Agent Bird managed completion v1\n"+input), &out)
	return out.Bytes(), stop, err
}

func TestC04AGYSQLiteFixtureRoundTrip(t *testing.T) {
	cfg, args, path := c04SQLiteUnitScope(t)
	if c04agy.InterruptedPrompt != interruptedContinuationPrompt {
		t.Fatal("fixture interrupted prompt drifted from the real continuation")
	}
	for cycle := 1; cycle <= 3; cycle++ {
		var before os.FileInfo
		input := "Original bounded task"
		if cycle > 1 {
			var err error
			before, err = os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			input = "answer-" + strconv.Itoa(cycle-1)
		}
		argv := append([]string(nil), args...)
		if cycle > 1 {
			argv = append(argv, "--conversation", c04agy.SessionID)
		}
		body, stop, err := c04SQLiteUnitRun(t, cfg, argv, input)
		if err != nil || stop {
			t.Fatal("SQLite continuation failed", cycle, stop, err)
		}
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(lines) != 2 {
			t.Fatal("missing exact init/terminal")
		}
		e, err := adapter.ParseEvent(adapter.ProviderAGY, lines[1], 4096)
		completion, completionErr := adapter.DecodeTaskCompletion(e.StructuredText, 4096)
		want := "technical_question"
		if cycle == 3 {
			want = "result"
		}
		if err != nil || completionErr != nil || e.SessionID != c04agy.SessionID || completion.Kind != want {
			t.Fatal("native structured completion changed", e, completion, err, completionErr)
		}
		after, err := os.Stat(path)
		if err != nil || after.Mode().Perm() != 0600 || (before != nil && !os.SameFile(before, after)) {
			t.Fatal("SQLite save replaced its original inode", err)
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
				t.Fatal("SQLite remained open/sidecar present", suffix, err)
			}
		}
		u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			t.Fatal(err)
		}
		var session, cwd string
		var saved int
		err = db.QueryRow(`SELECT session_id,cwd,cycle FROM fixture_session WHERE singleton=1`).Scan(&session, &cwd, &saved)
		closeErr := db.Close()
		if err != nil || closeErr != nil || session != c04agy.SessionID || cwd != cfg.CWD || saved != cycle {
			t.Fatal("committed state was not reopened", err, closeErr, saved)
		}
	}
}

func TestC04AGYSQLiteFixtureInterruptedPreservesDirtyWork(t *testing.T) {
	cfg, args, _ := c04SQLiteUnitScope(t)
	cfg.Role, cfg.Mode = "implementer", "interrupted"
	out, stop, err := c04SQLiteUnitRun(t, cfg, args, "Original bounded task")
	if err != nil || !stop || strings.Count(string(out), "\n") != 1 {
		t.Fatal("initial interrupted fixture did not request original Host Stop", stop, err)
	}
	args = append(args, "--conversation", c04agy.SessionID)
	out, stop, err = c04SQLiteUnitRun(t, cfg, args, interruptedContinuationPrompt)
	if err != nil || stop || !bytes.Contains(out, []byte(`"kind":"result"`)) {
		t.Fatal("explicit pending work failed to continue", stop, err)
	}
	if body, err := os.ReadFile(filepath.Join(cfg.CWD, "owned")); err != nil || string(body) != "dirty-2\n" {
		t.Fatal("original dirty work was not retained", err)
	}
}

func c04FixtureLaunchFromStore(t *testing.T, ctx context.Context, state, run, task string) string {
	t.Helper()
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var launch string
	err = db.SQL().QueryRowContext(ctx, `SELECT r.host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=? AND t.id=?`, run, task).Scan(&launch)
	if err != nil || launch == "" || launch == "." || launch == ".." || filepath.Base(launch) != launch || strings.ContainsAny(launch, "\\\x00\r\n") {
		t.Fatal("original run/task durable launch unavailable", err)
	}
	return launch
}

func c04AssertQuestionLaunch(t *testing.T, ctx context.Context, state, launch string, q contract.Event) {
	t.Helper()
	if launch == "" || q.Kind != contract.EventQuestion || q.ProducerID != launch || q.RunID != "run" || q.TaskID != "task" || q.AttemptID == "" || q.SegmentID == "" || q.WorkRevision != 1 {
		t.Fatal("Question does not belong to the original fixture launch")
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	err = db.SQL().QueryRowContext(ctx, `SELECT e.body_json FROM runtime_events e JOIN tasks t ON t.id=e.task_id JOIN task_runtime r ON r.task_id=e.task_id JOIN segment_runtime s ON s.segment_id=e.segment_id JOIN attempts a ON a.id=s.attempt_id AND a.id=e.attempt_id AND a.task_id=e.task_id WHERE e.event_id=? AND t.run_id=? AND e.task_id=? AND r.host_launch_id=? AND s.host_id=? AND e.producer_id=? AND e.attempt_id=? AND e.segment_id=?`, q.EventID, q.RunID, q.TaskID, launch, launch, launch, q.AttemptID, q.SegmentID).Scan(&body)
	var original contract.Event
	if err != nil || json.Unmarshal([]byte(body), &original) != nil || original.Kind != q.Kind || original.ProducerID != q.ProducerID || original.RunID != q.RunID || original.TaskID != q.TaskID || original.AttemptID != q.AttemptID || original.SegmentID != q.SegmentID || original.WorkRevision != q.WorkRevision {
		t.Fatal("Question tuple differs from its original durable segment", err)
	}
}

func c04WaitSegmentSettled(t *testing.T, ctx context.Context, state, segment string) {
	t.Helper()
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for {
		var status, budget string
		err = db.SQL().QueryRowContext(ctx, `SELECT s.status,b.status FROM segment_runtime s JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.segment_id=?`, segment).Scan(&status, &budget)
		if err != nil {
			t.Fatal(err)
		}
		if status == "exited" && budget == "settled" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("original segment Exited/settlement barrier incomplete", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func c04ChangeOwnedAGYDatabase(t *testing.T, ctx context.Context, state, home, launch string, q contract.Event) []byte {
	t.Helper()
	c04AssertQuestionLaunch(t, ctx, state, launch, q)
	sealPath := filepath.Join(state, "host-spool", launch, q.AttemptID, q.SegmentID, "agy-session-seal.json")
	seal, err := os.ReadFile(sealPath)
	if err != nil || len(seal) == 0 || len(seal) > 16*1024 {
		t.Fatal("original completed Question seal unavailable", err)
	}
	component := "agent-bird-" + hex.EncodeToString(c04DigestBytes([]byte(q.RunID + "\x00" + q.TaskID + "\x00" + q.AttemptID + "\x00" + strconv.Itoa(q.WorkRevision))))[:48]
	path := filepath.Join(home, ".gemini", component, "conversations", c04agy.SessionID+".db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE fixture_changed_after_original_exit(x INTEGER)`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	after, err := os.ReadFile(path)
	if err != nil || bytes.Equal(before, after) {
		t.Fatal("owned database mutation did not change its bytes", err)
	}
	t.Logf("C04_HISTORY_CHANGED original_seal_sha256=%x before_sha256=%x after_sha256=%x", sha256.Sum256(seal), sha256.Sum256(before), sha256.Sum256(after))
	return seal
}

func c04AssertChangedHistoryRefused(t *testing.T, ctx context.Context, state, launch string, q contract.Event, originalSeal []byte) {
	t.Helper()
	c04AssertQuestionLaunch(t, ctx, state, launch, q)
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var spawned, attempts, rejected int
	if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='spawned'`).Scan(&spawned); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM segment_runtime s JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.status='not_started' AND s.outcome='launch_rejected' AND b.status='settled' AND b.used_ms=0`).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if spawned != 1 || attempts != 1 || rejected != 1 {
		t.Fatal("changed history spawned/restarted/charged a new worker", spawned, attempts, rejected)
	}
	seal, err := os.ReadFile(filepath.Join(state, "host-spool", launch, q.AttemptID, q.SegmentID, "agy-session-seal.json"))
	if err != nil || !bytes.Equal(seal, originalSeal) {
		t.Fatal("resume repaired the original completed receipt", err)
	}
	t.Logf("C04_HISTORY_REFUSAL spawned=%d attempts=%d rejected_settled_zero=%d original_seal_sha256=%x", spawned, attempts, rejected, sha256.Sum256(seal))
}

func c04LogAGYSeals(t *testing.T, state, launch string) int {
	t.Helper()
	if launch == "" {
		t.Fatal("empty launch cannot select the whole fixture spool")
	}
	count := 0
	err := filepath.WalkDir(filepath.Join(state, "host-spool", launch), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		if entry.IsDir() || entry.Name() != "agy-session-seal.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16*1024 {
			return errors.New("c04_fixture_seal_untrusted")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Protected metadata carries tuple/process/file identities and digests,
		// never provider text, config contents or control/report capabilities.
		t.Logf("C04_AGY_SEAL path=%s sha256=%x record=%s", path, sha256.Sum256(body), body)
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestC04AGYSQLiteFixtureResumeRejectsDrift(t *testing.T) {
	for _, name := range []string{"missing", "session", "cwd", "answer", "restart"} {
		t.Run(name, func(t *testing.T) {
			cfg, args, path := c04SQLiteUnitScope(t)
			if _, _, err := c04SQLiteUnitRun(t, cfg, args, "Original bounded task"); err != nil {
				t.Fatal(err)
			}
			if name == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if name == "session" || name == "cwd" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				query := `UPDATE fixture_session SET session_id='foreign'`
				if name == "cwd" {
					query = `UPDATE fixture_session SET cwd='foreign'`
				}
				_, err = db.Exec(query)
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			}
			argv, input := append([]string(nil), args...), "answer-1"
			if name != "restart" {
				argv = append(argv, "--conversation", c04agy.SessionID)
			}
			if name == "answer" {
				input = "no answer"
			}
			out, _, err := c04SQLiteUnitRun(t, cfg, argv, input)
			if err == nil || len(out) != 0 {
				t.Fatal("drift reached init/terminal", err)
			}
			if name == "missing" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("missing resume restarted a database", err)
				}
			}
		})
	}
}

func TestC04AGYFixtureSchemaAndScopeRejectBeforeState(t *testing.T) {
	for _, name := range []string{"schema_missing", "schema_bad", "schema_other", "schema_duplicate", "schema_large", "path_missing", "path_absolute", "path_parent", "path_duplicate"} {
		t.Run(name, func(t *testing.T) {
			cfg, args, path := c04SQLiteUnitScope(t)
			switch name {
			case "schema_missing":
				args = args[:2]
			case "schema_bad":
				args[3] = "{"
			case "schema_other":
				args[3] = `{}`
			case "schema_duplicate":
				args = append(args, "--json-schema", args[3])
			case "schema_large":
				args[3] = strings.Repeat("x", 4097)
			case "path_missing":
				args = args[2:]
			case "path_absolute":
				args[1] = cfg.Home
			case "path_parent":
				args[1] = "../foreign"
			case "path_duplicate":
				args = append(args, "--app_data_dir", args[1])
			}
			out, _, err := c04SQLiteUnitRun(t, cfg, args, "Original bounded task")
			if err == nil || len(out) != 0 {
				t.Fatal("invalid schema/scope reached worker state", err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("invalid request allocated a database", err)
			}
		})
	}
}

func TestC04AGYFixtureOverlayRejectsInvalidAuthority(t *testing.T) {
	valid := []byte("package host\nconst agyHistoryVersion = \"1.2.16\"\nconst agyHistorySHA256 = \"" + c04OriginalAGYPin + "\"\n")
	digest := strings.Repeat("a", 64)
	for _, name := range []string{"valid", "missing", "wrong_pin", "var", "duplicate", "short", "uppercase"} {
		t.Run(name, func(t *testing.T) {
			original, replacement := bytes.Clone(valid), digest
			switch name {
			case "missing":
				original = []byte("package host\n")
			case "wrong_pin":
				original = bytes.ReplaceAll(original, []byte(c04OriginalAGYPin), []byte(strings.Repeat("b", 64)))
			case "var":
				original = bytes.Replace(original, []byte("const agyHistorySHA256"), []byte("var agyHistorySHA256"), 1)
			case "duplicate":
				original = append(original, []byte("const agyHistorySHA256 = \""+c04OriginalAGYPin+"\"\n")...)
			case "short":
				replacement = "a"
			case "uppercase":
				replacement = strings.Repeat("A", 64)
			}
			result, err := c04AGYPinOverlay(original, replacement)
			if name == "valid" {
				if err != nil || !bytes.Equal(result, bytes.Replace(valid, []byte(c04OriginalAGYPin), []byte(digest), 1)) || !bytes.Equal(original, valid) {
					t.Fatal("single-const overlay changed another byte or its source", err)
				}
			} else if err == nil || result != nil {
				t.Fatal("invalid overlay authority accepted", err)
			}
		})
	}
}
