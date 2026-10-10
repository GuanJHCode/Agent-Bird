// Controlled native review fixture: reads only its current worktree and emits
// Claude's structured terminal envelope. It never calls a provider or network.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	prompt, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	cwd, cwdErr := os.Getwd()
	if err != nil || cwdErr != nil || !strings.Contains(string(prompt), "immutable candidate at "+cwd+".") {
		fmt.Fprintln(os.Stderr, "bound review prompt missing")
		os.Exit(70)
	}
	b, err := os.ReadFile("file")
	if err != nil || string(b) != "candidate\n" {
		fmt.Fprintln(os.Stderr, "rendered candidate unavailable")
		os.Exit(71)
	}
	if err := os.WriteFile("file", []byte("reviewer-write\n"), 0600); !errors.Is(err, os.ErrPermission) {
		fmt.Fprintln(os.Stderr, "tracked write did not return permission denial")
		os.Exit(72)
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"controlled-review"}`)
	fmt.Println(`{"type":"result","subtype":"success","session_id":"controlled-review","result":"native commentary","structured_output":{"decision":"approve","summary":"read candidate; tracked write denied"}}`)
}
