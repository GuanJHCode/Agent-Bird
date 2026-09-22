package host

import (
	"reflect"
	"testing"
)

func TestCodexWorkerParentRetainsEveryMCPRestriction(t *testing.T) {
	home := &codexHome{path: "/private/task/home", runtime: "/private/task/runtime"}
	config := map[string]any{"mcp_servers": map[string]any{
		"http":  map[string]any{"enabled": true, "url": "https://example.invalid/mcp"},
		"stdio": map[string]any{"enabled": true, "command": "/bin/sh"},
	}}
	args, err := codexWorkerParentArgs(home, config)
	if err != nil {
		t.Fatal(err)
	}
	want := append(codexRuntimeFlags(home), "-c", `mcp_servers."http".enabled=false`, "-c", `mcp_servers."stdio".enabled=false`, "app-server", "--listen", "stdio://")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("parent discarded source restrictions: %q", args)
	}
}
