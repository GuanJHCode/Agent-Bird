// Package fixture supplies only the offline C04 worker's owned SQLite state.
package fixture

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	_ "modernc.org/sqlite"
)

const SessionID = "c04-session"
const InterruptedPrompt = "Continue this session's interrupted attempt. Inspect its current state and preserve existing changes. Check whether the previous operation already completed before taking another action. Continue only pending authorized work; do not repeat completed side effects or widen task scope or permissions."

type Config struct {
	Home, CWD, Role, Mode string
}

type State struct {
	SessionID, CWD string
	Cycle          int
}

func options(args []string) (component, resume string, err error) {
	seen := map[string]bool{}
	schema := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--app_data_dir=") || strings.HasPrefix(arg, "--json-schema=") || strings.HasPrefix(arg, "--conversation=") {
			return "", "", errors.New("c04_fixture_option_invalid")
		}
		if arg != "--app_data_dir" && arg != "--json-schema" && arg != "--conversation" {
			continue
		}
		if seen[arg] || i+1 == len(args) || args[i+1] == "" {
			return "", "", errors.New("c04_fixture_option_invalid")
		}
		seen[arg] = true
		i++
		switch arg {
		case "--app_data_dir":
			component = args[i]
		case "--conversation":
			resume = args[i]
		case "--json-schema":
			schema = args[i]
		}
	}
	var parsed any
	if !regexp.MustCompile(`^agent-bird-[0-9a-f]{48}$`).MatchString(component) || len(schema) > 4096 || !utf8.ValidString(schema) || json.Unmarshal([]byte(schema), &parsed) != nil || schema != adapter.ManagedTaskSchema() || (seen["--conversation"] && resume != SessionID) {
		return "", "", errors.New("c04_fixture_option_invalid")
	}
	return component, resume, nil
}

func directory(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	st, statErr := os.Lstat(path)
	if err != nil || statErr != nil || canonical != path || !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("c04_fixture_directory_untrusted")
	}
	return nil
}

func noSidecars(path string) error {
	for _, name := range []string{strings.TrimSuffix(path, ".db") + ".pb", path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return errors.New("c04_fixture_sidecar_present")
		}
	}
	return nil
}

func openDatabase(ctx context.Context, path string, creating bool) (*sql.DB, error) {
	if creating {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return nil, errors.New("c04_fixture_resume_database_missing_or_untrusted")
	}
	if err := noSidecars(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	journal := ""
	query := "PRAGMA journal_mode"
	if creating {
		query += "=DELETE"
	}
	if err = db.QueryRowContext(ctx, query).Scan(&journal); err != nil || journal != "delete" {
		_ = db.Close()
		return nil, errors.New("c04_fixture_journal_mode_invalid")
	}
	return db, nil
}

func save(ctx context.Context, db *sql.DB, state State, creating bool) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if creating {
		if _, err = tx.ExecContext(ctx, `CREATE TABLE fixture_session(singleton INTEGER PRIMARY KEY CHECK(singleton=1),session_id TEXT NOT NULL,cwd TEXT NOT NULL,cycle INTEGER NOT NULL CHECK(cycle>0))`); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO fixture_session VALUES(1,?,?,?)`, state.SessionID, state.CWD, state.Cycle)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE fixture_session SET cycle=? WHERE singleton=1 AND session_id=? AND cwd=?`, state.Cycle, state.SessionID, state.CWD)
		if err == nil {
			var rows int64
			rows, err = result.RowsAffected()
			if err == nil && rows != 1 {
				err = errors.New("c04_fixture_session_changed")
			}
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Run uses explicit synthetic roots. true means the executable must remain
// alive after init for the original Host Stop; the pure helper starts no actor.
func Run(ctx context.Context, cfg Config, args []string, input io.Reader, output io.Writer) (bool, error) {
	component, resume, err := options(args)
	if err != nil {
		return false, err
	}
	if (cfg.Role != "reviewer" && cfg.Role != "implementer") || (cfg.Mode != "question" && cfg.Mode != "interrupted") || !filepath.IsAbs(cfg.Home) || filepath.Clean(cfg.Home) != cfg.Home || !filepath.IsAbs(cfg.CWD) || filepath.Clean(cfg.CWD) != cfg.CWD {
		return false, errors.New("c04_fixture_scope_invalid")
	}
	for _, path := range []string{cfg.Home, filepath.Join(cfg.Home, ".gemini"), filepath.Join(cfg.Home, ".gemini", component)} {
		if err = directory(path); err != nil {
			return false, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(input, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || !bytes.Contains(data, []byte("Agent Bird managed completion v1")) || bytes.Contains(data, []byte("Agent Bird technical-question protocol v1")) {
		return false, errors.New("c04_fixture_input_invalid")
	}
	conversations := filepath.Join(cfg.Home, ".gemini", component, "conversations")
	if resume == "" {
		if err = os.Mkdir(conversations, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return false, err
		}
	}
	if err = directory(conversations); err != nil {
		return false, err
	}
	path := filepath.Join(conversations, SessionID+".db")
	db, err := openDatabase(ctx, path, resume == "")
	if err != nil {
		return false, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	state := State{SessionID: SessionID, CWD: cfg.CWD}
	if resume != "" {
		if err = db.QueryRowContext(ctx, `SELECT session_id,cwd,cycle FROM fixture_session WHERE singleton=1`).Scan(&state.SessionID, &state.CWD, &state.Cycle); err != nil || state.SessionID != resume || state.CWD != cfg.CWD || state.Cycle < 1 || state.Cycle > 2 {
			return false, errors.New("c04_fixture_session_changed")
		}
		if cfg.Mode == "interrupted" {
			if !bytes.Contains(data, []byte(InterruptedPrompt)) || bytes.Contains(data, []byte("Original bounded task")) {
				return false, errors.New("c04_fixture_continuation_prompt_invalid")
			}
		} else if !bytes.Contains(data, []byte(fmt.Sprintf("answer-%d", state.Cycle))) {
			return false, errors.New("c04_fixture_answer_missing")
		}
	}
	if cfg.Role == "implementer" {
		owned := filepath.Join(cfg.CWD, "owned")
		if state.Cycle > 0 {
			body, err := os.ReadFile(owned)
			if err != nil || !strings.Contains(string(body), fmt.Sprintf("dirty-%d", state.Cycle)) {
				return false, errors.New("c04_fixture_dirty_work_lost")
			}
		}
		if err = os.WriteFile(owned, []byte(fmt.Sprintf("dirty-%d\n", state.Cycle+1)), 0600); err != nil {
			return false, err
		}
	}
	state.Cycle++
	if err = save(ctx, db, state, resume == ""); err != nil {
		return false, err
	}
	err = db.Close()
	closed = true
	if err != nil {
		return false, err
	}
	if err = noSidecars(path); err != nil {
		return false, err
	}
	if _, err = fmt.Fprintf(output, "{\"event\":\"init\",\"conversation_id\":%q}\n", state.SessionID); err != nil {
		return false, err
	}
	if cfg.Mode == "interrupted" && state.Cycle == 1 {
		return true, nil
	}
	kind, text := "result", "Completed after two answers"
	if cfg.Mode == "question" && state.Cycle <= 2 {
		kind, text = "technical_question", fmt.Sprintf("Technical question %d", state.Cycle)
	}
	structured, err := json.Marshal(map[string]any{"version": 1, "kind": kind, "text": text})
	if err != nil {
		return false, err
	}
	_, err = fmt.Fprintf(output, "{\"event\":\"result\",\"result\":{\"conversation_id\":%q,\"status\":\"SUCCESS\",\"response\":\"native aggregate commentary\",\"structured_output\":%s}}\n", state.SessionID, structured)
	return false, err
}
