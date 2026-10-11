// Run only in the offline, unprivileged Linux acceptance container.
package main

import (
	"archive/tar"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	if runtime.GOOS != "linux" || os.Geteuid() != 65534 {
		panic("offline Linux nobody actor required")
	}
	root, err := os.MkdirTemp("", "linux-runtime-status-")
	must(err)
	defer os.RemoveAll(root)
	plugin := filepath.Join(root, "plugin")
	must(os.Mkdir(plugin, 0700))
	f, err := os.Open("/input/package.tar")
	must(err)
	defer f.Close()
	r := tar.NewReader(f)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		must(err)
		if h.Typeflag != tar.TypeReg || filepath.IsAbs(h.Name) || filepath.Clean(h.Name) != h.Name || strings.HasPrefix(h.Name, "../") {
			panic("invalid fixture")
		}
		p := filepath.Join(plugin, h.Name)
		must(os.MkdirAll(filepath.Dir(p), 0700))
		out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode))
		must(err)
		_, err = io.Copy(out, r)
		must(err)
		must(out.Close())
	}
	calls := 0
	call := func(install, state, action string, extra ...string) ([]byte, error) {
		calls++
		args := []string{"plugin-run", "--plugin-root", plugin, "--install-root", install, "--state-dir", state, "--", action}
		args = append(args, extra...)
		cmd := exec.Command("/test/codex-orchestrator", args...)
		out, err := cmd.CombinedOutput()
		fmt.Printf("COMMAND %d %q\nOUTPUT %s", calls, args, out)
		return out, err // CombinedOutput waits the original child.
	}
	install, state := filepath.Join(root, "install"), filepath.Join(root, "state")
	// Check installs the same closed collect-only package without creating state.
	out, err := call(install, state, "check")
	must(err)
	var check struct {
		Binary string `json:"binary"`
		Status string `json:"status"`
	}
	must(json.Unmarshal(out, &check))
	if check.Status != "ready" || !strings.HasPrefix(check.Binary, install+"/versions/") {
		panic("not privately installed")
	}
	must(os.Mkdir(state, 0700))
	db, err := sql.Open("sqlite", filepath.Join(state, "state.db"))
	must(err)
	for _, q := range []string{
		`CREATE TABLE segment_runtime(status TEXT)`, `CREATE TABLE stop_runtime(status TEXT)`,
		`CREATE TABLE tasks(id TEXT,run_id TEXT,status TEXT)`, `CREATE TABLE host_launches(status TEXT)`,
		`CREATE TABLE runtime_hosts(status TEXT)`, `CREATE TABLE runtime_events(delivery_status TEXT)`,
		`CREATE TABLE report_events(delivery_status TEXT)`, `CREATE TABLE runs(id TEXT)`, `CREATE TABLE attempts(task_id TEXT)`,
		`INSERT INTO tasks VALUES('t','existing','ready')`, `INSERT INTO host_launches VALUES('reserved')`,
		`INSERT INTO runs VALUES('existing')`, `INSERT INTO attempts VALUES('t')`,
	} {
		_, err = db.Exec(q)
		must(err)
	}
	must(db.Close())
	must(os.Chmod(filepath.Join(state, "state.db"), 0600))
	snapshot := func() map[string]string {
		result := map[string]string{}
		for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
			raw, err := os.ReadFile(filepath.Join(state, name))
			if os.IsNotExist(err) {
				result[name] = "absent"
				continue
			}
			must(err)
			result[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
		}
		return result
	}
	before := snapshot()
	// Both flags are intentional: launcher state is not automatically forwarded.
	out, err = call(install, state, "runtime-status", "--state-dir", state, "--run-id", "existing")
	must(err)
	var status map[string]any
	must(json.Unmarshal(out, &status))
	if status["observation_only"] != true || status["queued_tasks"] != float64(1) || status["unreleased_hosts"] != float64(1) || status["matching_runs"] != float64(1) || status["matching_tasks"] != float64(1) || status["matching_attempts"] != float64(1) {
		panic("invalid observation")
	}
	after := snapshot()
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		panic("DB/WAL/SHM changed")
	}
	missing := filepath.Join(root, "missing-state")
	out, err = call(install, missing, "runtime-status", "--state-dir", missing)
	if err == nil || !strings.Contains(string(out), "runtime_unavailable") {
		panic("missing DB admitted")
	}
	if _, err = os.Lstat(missing); !os.IsNotExist(err) {
		panic("missing state created")
	}
	for _, action := range []string{"provider", "config-apply", "collect", "controller", "serve", "unknown"} {
		dest, state := filepath.Join(root, "deny-"+action), filepath.Join(root, "deny-state-"+action)
		out, err = call(dest, state, action)
		if err == nil || !strings.Contains(string(out), "portable_action_unsupported") {
			panic("action admitted")
		}
		for _, p := range []string{dest, state} {
			if _, err = os.Lstat(p); !os.IsNotExist(err) {
				panic("refusal created files")
			}
		}
	}
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "passed", "command_count": calls, "installed_binary": check.Binary, "before": before, "after": after, "missing_state_created": false, "observation": status}))
}
