// Offline C04 executable. No provider, account, network or model invocation.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/cmd/orchestrator/testdata/c04agy/fixture"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("1.2.16")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--print --input-format --output-format --mode --conversation --model --effort --json-schema")
		return
	}
	cwd, err := os.Getwd()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var stop bool
		stop, err = fixture.Run(ctx, fixture.Config{Home: os.Getenv("HOME"), CWD: cwd, Role: os.Getenv("C04_FIXTURE_ROLE"), Mode: os.Getenv("C04_FIXTURE_MODE")}, os.Args[1:], os.Stdin, os.Stdout)
		cancel()
		if err == nil && stop {
			// Preserve the original fixture's finite wait. Only Host Stop may
			// produce the expected interrupted outcome before this failure.
			time.Sleep(30 * time.Second)
			err = fmt.Errorf("explicit interrupted tree was not stopped")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "c04_fixture_failure:", err)
		os.Exit(19)
	}
}
