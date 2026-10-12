// Owned offline worker protocol fixture; it never invokes a real model.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("acp-public-worker-v1")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--output-format --input-format --permission-mode --disallowed-tools --model --effort --json-schema")
		return
	}
	for _, arg := range os.Args[1:] {
		if arg == "--print" || arg == "-p" {
			fmt.Println(`{"type":"system","subtype":"init","session_id":"owned-public-worker"}`)
			fmt.Println(`{"type":"result","subtype":"success","session_id":"owned-public-worker","is_error":false,"permission_denials":[],"result":"owned result","structured_output":{"version":1,"kind":"result","text":"owned result"}}`)
			return
		}
	}
	os.Exit(20)
}
