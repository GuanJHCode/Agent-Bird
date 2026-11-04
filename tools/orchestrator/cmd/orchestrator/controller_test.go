package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestControllerStartMissingCLILeavesStateAbsent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", filepath.Join(root, "empty"))
	state := filepath.Join(root, "state")
	err := run(context.Background(), []string{"controller", "start", "--provider", "claude", "--state-dir", state}, os.Stdout, os.Stderr)
	if err == nil || err.Error() != "provider_binary_not_found" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("allocated runtime before checking CLI")
	}
}

func TestControllerDoesNotStartCLIWhenOwnerBindingFails(t *testing.T) {
	root := t.TempDir()
	prepareControllerSkill(t, root)
	marker := filepath.Join(root, "started")
	cli := filepath.Join(root, "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state-file")
	if err := os.WriteFile(state, []byte("not a directory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	err := run(context.Background(), []string{"controller", "start", "--provider", "claude", "--state-dir", state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md")}, os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("owner binding unexpectedly succeeded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("provider CLI started before owner binding succeeded")
	}
}

func TestControllerStartBindsManagedProcessScope(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "controller-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "orchestrator")
	prepareControllerSkill(t, root)
	t.Cleanup(func() {
		state := filepath.Join(root, "state")
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	})
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	state := filepath.Join(root, "state")
	cli := filepath.Join(root, "claude")
	commandPath, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(root, "skills", "orchestrate", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("managed controller skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
	portableSkillPath, err := filepath.EvalSymlinks(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ntest -z \"$CODEX_THREAD_ID\" || exit 34\ntest \"$AGENT_BIRD_COMMAND\" = \"" + commandPath + "\" || exit 31\ntest \"$AGENT_BIRD_SKILL\" = \"" + portableSkillPath + "\" || exit 32\ntest -n \"$AGENT_BIRD_OWNER_REQUEST\" || exit 33\nexec \"" + bin + "\" owner-bind --current --state-dir \"" + state + "\"\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "claude", "--state-dir", state)
	cmd.Env = environmentWith(environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH")), "AGENT_BIRD_SKILL", "/stale/skill")
	cmd.Env = environmentWith(cmd.Env, "CODEX_THREAD_ID", "00000000-0000-0000-0000-000000000001")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("controller: %s %v", out, err)
	}
	if !strings.Contains(string(out), `"owner_mode":"local"`) || !strings.Contains(string(out), `"controller_thread":"managed-claude-`) {
		t.Fatalf("managed owner output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err != nil {
		t.Fatalf("expected owner bind runtime: %v", err)
	}
}

func TestControllerTerminationStopsManagedCLI(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "controller-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin", "orchestrator")
	prepareControllerSkill(t, root)
	t.Cleanup(func() {
		state := filepath.Join(root, "state")
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	})
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	pidPath, readyPath := filepath.Join(root, "cli.pid"), filepath.Join(root, "ready")
	cli := filepath.Join(root, "claude")
	script := "#!/bin/sh\necho $$ > \"" + pidPath + "\"\ntouch \"" + readyPath + "\"\ntrap 'exit 0' TERM INT HUP\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "controller", "start", "--provider", "claude")
	state := filepath.Join(root, "state")
	cmd.Args = append(cmd.Args, "--state-dir", state)
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed CLI did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("controller unexpectedly reported clean exit after termination")
	}
	deadline = time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err = syscall.Kill(pid, 0); err == nil {
		t.Fatal("managed CLI survived controller termination")
	}
}

func TestControllerEscalatesWhenManagedCLIIgnoresTermination(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "controller-kill-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin", "orchestrator")
	prepareControllerSkill(t, root)
	t.Cleanup(func() {
		state := filepath.Join(root, "state")
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	})
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ready := filepath.Join(root, "ready")
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\ntouch \""+ready+"\"\ntrap '' TERM\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "controller", "start", "--provider", "claude", "--state-dir", filepath.Join(root, "state"))
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed CLI did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := cmd.Wait(); err == nil {
		t.Fatal("controller unexpectedly reported clean exit after termination")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("termination escalation took %s", elapsed)
	}
}

func TestControllerForegroundsInteractiveCLI(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "controller-pty-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin", "orchestrator")
	prepareControllerSkill(t, root)
	t.Cleanup(func() {
		state := filepath.Join(root, "state")
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	})
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	result := filepath.Join(root, "line")
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nIFS= read -r line\nprintf '%s' \"$line\" > \""+result+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	python := fmt.Sprintf(`import fcntl, os, pty, select, subprocess, termios, time
master, slave = pty.openpty()
def child():
    os.setsid()
    fcntl.ioctl(0, termios.TIOCSCTTY, 0)
env = dict(os.environ)
env["PATH"] = %q + ":" + env["PATH"]
p = subprocess.Popen([%q, "controller", "start", "--provider", "claude", "--state-dir", %q], stdin=slave, stdout=slave, stderr=slave, preexec_fn=child, env=env, close_fds=True)
os.close(slave)
deadline = time.time() + 5
while time.time() < deadline and os.tcgetpgrp(master) == p.pid:
    time.sleep(.02)
if os.tcgetpgrp(master) == p.pid:
    p.kill()
    raise SystemExit("controller did not foreground CLI process group")
os.write(master, b"keyboard-input\n")
deadline = time.time() + 8
while time.time() < deadline and not os.path.exists(%q):
    time.sleep(.02)
if not os.path.exists(%q):
    os.set_blocking(master, False)
    try:
        diagnostic = os.read(master, 65536)
    except BlockingIOError:
        diagnostic = b""
    process = subprocess.check_output(["/bin/ps", "-o", "pid=,ppid=,pgid=,tpgid=,state=,command=", "-p", str(p.pid)]).decode()
    group = subprocess.check_output(["/bin/ps", "-o", "pid=,ppid=,pgid=,tpgid=,state=,command=", "-g", str(os.tcgetpgrp(master))]).decode()
    p.kill()
    raise SystemExit("interactive CLI did not read terminal input: " + repr(diagnostic) + " status=" + repr(p.poll()) + " process=" + process + " group=" + group)
# Drain the terminal as an actual terminal emulator does, including EOF.
os.set_blocking(master, False)
deadline = time.time() + 5
while p.poll() is None and time.time() < deadline:
    if select.select([master], [], [], .05)[0]:
        try:
            if not os.read(master, 65536):
                os.close(master)
                break
        except OSError:
            os.close(master)
            break
try:
    status = p.wait(timeout=5)
except subprocess.TimeoutExpired:
    diagnostic = subprocess.check_output(["/bin/ps", "-o", "pid=,ppid=,pgid=,tpgid=,state=,command=", "-p", str(p.pid)]).decode()
    p.kill()
    raise SystemExit("controller wait failed: " + diagnostic)
if status != 0:
    raise SystemExit("controller failed")
`, root, bin, filepath.Join(root, "state"), result, result)
	if out, err := exec.Command("python3", "-c", python).CombinedOutput(); err != nil {
		t.Fatalf("pty launcher: %s %v", out, err)
	}
	line, err := os.ReadFile(result)
	if err != nil || string(line) != "keyboard-input" {
		t.Fatalf("interactive input result %q: %v", line, err)
	}
}

func TestControllerReportsSurvivingGroup(t *testing.T) {
	for _, cancelLeader := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelLeader), func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "child")
			script := `/bin/sh -c 'trap "" TERM; echo $$ > "$1"; exec /bin/sleep 30' fixture "$1" &
while [ ! -f "$1" ]; do sleep .01; done
`
			if cancelLeader {
				script += "trap 'exit 0' TERM\nwhile :; do sleep 1; done\n"
			}
			cmd := exec.Command("/bin/sh", "-c", script, "fixture", marker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runControllerCLI(ctx, cmd) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child not started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL) // only this fixture's recorded child
			if cancelLeader {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || err.Error() != "controller_process_tree_unknown" {
					t.Fatalf("surviving child hidden: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("controller did not return bounded unknown")
			}
		})
	}
}

func prepareControllerSkill(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "skills", "orchestrate")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "orchestrate", "SKILL.md"), []byte("fixture skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestControllerRejectsMissingDefaultSkill(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	state := filepath.Join(root, "state")
	err := controllerEntry(context.Background(), []string{"start", "--provider", "claude", "--state-dir", state})
	if err == nil || err.Error() != "skill_path_unavailable" {
		t.Fatalf("missing default skill: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("allocated state before skill validation")
	}
}

func TestControllerRejectsUnreadableSkillBeforeBinding(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	if err := os.WriteFile(skill, []byte("unreadable"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(skill, 0600) })
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	state := filepath.Join(root, "state-file")
	if err := os.WriteFile(state, []byte("must not reach runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	err := controllerEntry(context.Background(), []string{"start", "--provider", "claude", "--skill-path", skill, "--state-dir", state})
	if err == nil || err.Error() != "skill_path_unavailable" {
		t.Fatalf("unreadable skill passed validation: %v", err)
	}
}
