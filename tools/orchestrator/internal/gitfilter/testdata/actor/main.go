package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(20)
	}
	switch os.Args[1] {
	case "copy", "xor":
		b := make([]byte, 32768)
		for {
			n, e := os.Stdin.Read(b)
			if n > 0 {
				if os.Args[1] == "xor" {
					for i := 0; i < n; i++ {
						b[i] ^= 0x5a
					}
				}
				if _, err := os.Stdout.Write(b[:n]); err != nil {
					os.Exit(21)
				}
			}
			if e == io.EOF {
				return
			}
			if e != nil {
				os.Exit(22)
			}
		}
	case "fail":
		os.Exit(7)
	case "hang":
		for {
			time.Sleep(time.Second)
		}
	case "overflow":
		b := make([]byte, 32768)
		for i := 0; i < 513; i++ {
			if _, e := os.Stdout.Write(b); e != nil {
				return
			}
		}
	case "audit":
		if len(os.Args) != 5 {
			os.Exit(23)
		}
		checks := map[string]string{}
		record := func(k string, e error) {
			if e == nil {
				checks[k] = "allowed"
			} else if errors.Is(e, syscall.EPERM) || errors.Is(e, syscall.EACCES) {
				checks[k] = "denied"
			} else {
				checks[k] = e.Error()
			}
		}
		_, e := os.ReadFile(os.Args[2])
		record("external_read", e)
		record("external_write", os.WriteFile(os.Args[2], []byte("changed"), 0600))
		record("empty_chmod", os.Chmod(os.Getenv("HOME"), 0700))
		record("empty_write", os.WriteFile(os.Getenv("HOME")+"/entry", []byte("data"), 0600))
		record("self_chmod", os.Chmod(os.Args[0], 0700))
		record("fork", exec.Command("/usr/bin/true").Run())
		c, e := net.DialTimeout("tcp", os.Args[3], time.Second)
		record("network", e)
		if c != nil {
			c.Close()
		}

		_ = json.NewEncoder(os.Stdout).Encode(checks)
	case "exec_other":
		e := syscall.Exec("/bin/echo", []string{"echo", "exec-allowed"}, os.Environ())
		if errors.Is(e, syscall.EPERM) || errors.Is(e, syscall.EACCES) {
			_, _ = os.Stdout.Write([]byte("exec-denied\n"))
			return
		}
		os.Exit(25)
	default:
		os.Exit(24)
	}
}
