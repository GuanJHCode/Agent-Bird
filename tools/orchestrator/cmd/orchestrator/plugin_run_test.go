package main

import (
	"bytes"
	"context"
	"testing"
)

func TestPluginRunRejectsUnsupportedActionBeforeInstall(t *testing.T) {
	err := run(context.Background(), []string{"plugin-run", "--plugin-root", "/missing", "--", "native-bridge"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "portable_action_unsupported" {
		t.Fatalf("got %v", err)
	}
}

func TestPluginRunAllowsOwnerScopedRouting(t *testing.T) {
	if !pluginActionAllowed("routing") {
		t.Fatal("portable routing entry was not allowlisted")
	}
}
