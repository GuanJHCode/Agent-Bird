// Controlled offline actor. It consumes the exact expected bound context and
// actual candidate R under the original Host readonly sandbox.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(69)
	}
	var expected, actual map[string]any
	if json.Unmarshal([]byte(os.Args[1]), &expected) != nil {
		os.Exit(69)
	}
	prompt, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	cwd, e := os.Getwd()
	prefix := "\nIndependently review candidate R at " + cwd + " against this Host-bound target context: "
	if err != nil || e != nil || !strings.HasPrefix(string(prompt), prefix) {
		fmt.Fprintln(os.Stderr, "target prompt absent")
		os.Exit(70)
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(string(prompt), prefix)))
	if decoder.Decode(&actual) != nil || !reflect.DeepEqual(actual, expected) {
		fmt.Fprintln(os.Stderr, "bound target context changed")
		os.Exit(71)
	}
	b, err := os.ReadFile("file")
	if err != nil || string(b) != "candidate\n" {
		fmt.Fprintln(os.Stderr, "candidate R unavailable")
		os.Exit(72)
	}
	target, ok := actual["target"].(map[string]any)
	if !ok {
		os.Exit(73)
	}
	path, ok := target["target_worktree_identity"].(string)
	if !ok || !filepath.IsAbs(path) {
		os.Exit(73)
	}
	for _, p := range []string{"file", filepath.Join(path, "file")} {
		if err = os.WriteFile(p, []byte("reviewer-write\n"), 0600); !errors.Is(err, os.ErrPermission) {
			fmt.Fprintln(os.Stderr, "candidate/target write was not denied")
			os.Exit(74)
		}
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"controlled-target-review"}`)
	fmt.Println(`{"type":"result","subtype":"success","session_id":"controlled-target-review","result":"plain commentary does not approve","structured_output":{"decision":"approve","summary":"read candidate R and exact target context; candidate and target writes denied"}}`)
}
