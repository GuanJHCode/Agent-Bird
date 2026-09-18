package process

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExactEnvironmentDoesNotInheritGitRouting(t *testing.T) {
	t.Setenv("GIT_DIR", "/test-untrusted-routing")
	var command Command
	if err := json.Unmarshal([]byte(`{"Path":"/bin/sh","Args":["-c","printf '%s' \"${GIT_DIR-unset}\""],"Env":["PATH=/usr/bin:/bin"],"ExactEnv":true}`), &command); err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(p.Output()) != "unset" {
		t.Fatalf("inherited routing: %q", p.Output())
	}
}
