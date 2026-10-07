package process

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestExactEnvironmentDoesNotInheritGitRouting(t *testing.T) {
	t.Setenv("GIT_DIR", "/test-untrusted-routing")
	path, err := json.Marshal(duplexCommand(t, "exact-env").Path)
	if err != nil {
		t.Fatal(err)
	}
	var command Command
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"Path":%s,"Args":["-test.run=^TestDuplexOSHelper$"],"Env":["C06_DUPLEX_HELPER=exact-env"],"ExactEnv":true}`, path)), &command); err != nil {
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
