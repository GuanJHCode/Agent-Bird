// An owned native actor for the Darwin filter sandbox feasibility test.
package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type outcome struct {
	Allowed bool   `json:"allowed"`
	Denied  bool   `json:"denied"`
	Error   string `json:"error,omitempty"`
}

func result(err error) outcome {
	o := outcome{Allowed: err == nil, Denied: os.IsPermission(err)}
	if err != nil {
		o.Error = err.Error()
	}
	return o
}

func main() {
	if len(os.Args) != 8 {
		os.Exit(20)
	}
	scratch, tcp, unix := os.Args[1], os.Args[2], os.Args[3]
	checks := map[string]outcome{}
	for i, name := range []string{"worktree", "common_git", "host_state", "external"} {
		path := os.Args[4+i]
		_, err := os.ReadFile(path)
		checks[name+"_read"] = result(err)
		checks[name+"_write"] = result(os.WriteFile(path, []byte("actor-write\n"), 0600))
	}
	checks["scratch_write"] = result(os.WriteFile(filepath.Join(scratch, "allowed"), []byte("scratch\n"), 0600))
	_, err := os.ReadFile(filepath.Join(scratch, "allowed"))
	checks["scratch_read"] = result(err)
	checks["hardlink"] = result(os.Link(os.Args[4], filepath.Join(scratch, "forbidden-link")))
	err = os.Symlink(os.Args[4], filepath.Join(scratch, "alias"))
	checks["scratch_symlink"] = result(err)
	if err == nil {
		_, err = os.ReadFile(filepath.Join(scratch, "alias"))
		checks["symlink_read"] = result(err)
		checks["symlink_write"] = result(os.WriteFile(filepath.Join(scratch, "alias"), []byte("actor-alias-write\n"), 0600))
	}
	for network, address := range map[string]string{"tcp": tcp, "unix": unix} {
		c, err := net.DialTimeout(network, address, time.Second)
		checks[network+"_connect"] = result(err)
		if c != nil {
			_ = c.Close()
		}
	}
	checks["fork_exec"] = result(exec.Command("/usr/bin/true").Run())
	body, err := json.Marshal(checks)
	if err != nil || os.WriteFile(filepath.Join(scratch, "checks.json"), body, 0600) != nil {
		os.Exit(21)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil || len(input) > 65536 {
		os.Exit(22)
	}
	for i := range input {
		input[i] ^= 0x5a
	}
	if _, err := os.Stdout.Write(input); err != nil {
		os.Exit(23)
	}
}
