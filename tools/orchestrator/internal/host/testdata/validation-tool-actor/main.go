// Controlled native fixture; no provider, model, network or subprocess.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(70)
	}
	switch os.Args[1] {
	case "produce":
		if err := os.WriteFile("file", []byte("candidate\n"), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(71)
		}
		fmt.Println("controlled candidate")
	case "validate":
		b, e := os.ReadFile("file")
		if e != nil || string(b) != "candidate\n" {
			fmt.Fprintln(os.Stderr, "wrong candidate", e)
			os.Exit(72)
		}
		fmt.Println("validated actual materialization")
	case "protect":
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			os.Exit(73)
		}
		self, err := os.Executable()
		if err != nil || len(os.Args) != 3 {
			os.Exit(74)
		}
		peer := os.Args[2]
		ops := []struct {
			name string
			run  func() error
		}{
			{"self_chmod", func() error { return os.Chmod(self, 0700) }},
			{"parent_chmod", func() error { return os.Chmod(filepath.Dir(self), 0700) }},
			{"ancestor_chmod", func() error { return os.Chmod(filepath.Dir(filepath.Dir(self)), 0755) }},
			{"peer_chmod", func() error { return os.Chmod(peer, 0700) }},
			{"peer_write", func() error { return os.WriteFile(peer, []byte("changed"), 0700) }},
			{"peer_unlink", func() error { return os.Remove(peer) }},
			{"self_unlink", func() error { return os.Remove(self) }},
		}
		result := map[string]int{}
		for _, op := range ops {
			err := op.run()
			code := 0
			if err != nil {
				code = -1
				if p, ok := err.(*os.PathError); ok {
					if n, ok := p.Err.(syscall.Errno); ok {
						code = int(n)
					}
				}
			}
			result[op.name] = code
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			os.Exit(75)
		}
	default:
		os.Exit(76)
	}
}
