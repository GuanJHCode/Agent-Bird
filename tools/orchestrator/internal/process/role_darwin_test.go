//go:build darwin

package process

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("ORCHESTRATOR_PROCESS_ROLE_TEST_CHILD") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexDaemonRoleUsesBoundedKernelArguments(t *testing.T) {
	executable, err := ExecutablePath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		daemon bool
	}{
		{"daemon", []string{"app-server", "daemon"}, true},
		{"daemon-pid-update-loop", []string{"app-server", "daemon", "pid-update-loop"}, true},
		{"daemon-pid-update-loop-extra", []string{"app-server", "daemon", "pid-update-loop", "--extra"}, false},
		{"daemon-similar", []string{"app-server", "daemon-extra", "pid-update-loop"}, false},
		{"remote-session", []string{"app-server", "--remote-control"}, false},
		{"extra-option", []string{"app-server", "daemon", "--extra"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(executable, tc.args...)
			cmd.Env = []string{"ORCHESTRATOR_PROCESS_ROLE_TEST_CHILD=1", "PRIVATE_ENV_SENTINEL=never-export-this"}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			got, err := IsCodexAppServerDaemon(cmd.Process.Pid, executable)
			if err != nil || got != tc.daemon {
				t.Fatalf("role=%v err=%v", got, err)
			}
			if _, err := IsCodexAppServerDaemon(cmd.Process.Pid, "/different/codex"); err == nil {
				t.Fatal("different kernel executable accepted")
			}
		})
	}
	for _, pid := range []int{0, -1} {
		if _, err := IsCodexAppServerDaemon(pid, executable); err == nil {
			t.Fatal("invalid pid accepted")
		}
	}
}

func TestCodexDaemonArgumentParserFailsClosed(t *testing.T) {
	path := "/private/codex"
	makeArgs := func(argc uint32, args ...string) []byte {
		data := make([]byte, 4)
		binary.LittleEndian.PutUint32(data, argc)
		data = append(data, []byte(path)...)
		data = append(data, 0, 0)
		for _, arg := range args {
			data = append(data, []byte(arg)...)
			data = append(data, 0)
		}
		return append(data, []byte("PRIVATE_ENV_SENTINEL=never-export-this\x00")...)
	}
	for _, argv0 := range []string{path, filepath.Base(path)} {
		got, err := codexDaemonArguments(makeArgs(3, argv0, "app-server", "daemon"), path)
		if err != nil || !got {
			t.Fatalf("exact role=%v err=%v", got, err)
		}
		got, err = codexDaemonArguments(makeArgs(4, argv0, "app-server", "daemon", "pid-update-loop"), path)
		if err != nil || !got {
			t.Fatalf("exact pid-update-loop role=%v err=%v", got, err)
		}
	}
	for _, data := range [][]byte{nil, makeArgs(0, path), makeArgs(5000, path), []byte{3, 0, 0, 0, 'x'}} {
		if _, err := codexDaemonArguments(data, path); err == nil {
			t.Fatal("malformed args accepted")
		}
	}
	for _, data := range [][]byte{makeArgs(3, "", "app-server", "daemon"), makeArgs(3, path, "app-server", "daemon-extra"), makeArgs(4, path, "app-server", "daemon", "--other"), makeArgs(4, path, "app-server", "daemon", "pid-update-loop-extra"), makeArgs(4, path, "app-server", "daemon-extra", "pid-update-loop"), makeArgs(5, path, "app-server", "daemon", "pid-update-loop", "--extra")} {
		if got, _ := codexDaemonArguments(data, path); got {
			t.Fatal("inexact role excluded")
		}
	}
}
