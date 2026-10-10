// Controlled native review fixture: reads only its current worktree and emits
// Claude's structured terminal envelope. It never calls a provider or network.
package main

import (
	"fmt"
	"os"
)

func main() {
	b, err := os.ReadFile("file")
	if err != nil || string(b) != "candidate\n" {
		fmt.Fprintln(os.Stderr, "rendered candidate unavailable")
		os.Exit(71)
	}
	if err := os.WriteFile("file", []byte("reviewer-write\n"), 0600); err == nil {
		fmt.Fprintln(os.Stderr, "sandbox permitted tracked write")
		os.Exit(72)
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"controlled-review"}`)
	fmt.Println(`{"type":"result","subtype":"success","session_id":"controlled-review","result":"native commentary","structured_output":{"decision":"approve","summary":"read candidate; tracked write denied"}}`)
}
