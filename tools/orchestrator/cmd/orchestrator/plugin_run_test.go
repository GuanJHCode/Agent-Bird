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

func TestPortableRunPolicy(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		action   string
		want     error
	}{
		{name: "linux check", platform: "linux", action: "check"},
		{name: "linux runtime status", platform: "linux", action: "runtime-status"},
		{name: "linux config apply", platform: "linux", action: "config-apply", want: codeError("portable_action_unsupported")},
		{name: "linux status", platform: "linux", action: "status"},
		{name: "linux summary", platform: "linux", action: "summary"},
		{name: "linux collect", platform: "linux", action: "collect"},
		{name: "linux wait events", platform: "linux", action: "wait-events"},
		{name: "linux provider", platform: "linux", action: "provider", want: codeError("portable_action_unsupported")},
		{name: "linux unknown", platform: "linux", action: "unknown", want: codeError("portable_action_unsupported")},
		{name: "darwin provider", platform: "darwin", action: "provider"},
		{name: "darwin unknown", platform: "darwin", action: "unknown", want: codeError("portable_action_unsupported")},
		{name: "windows", platform: "windows", action: "check", want: codeError("portable_platform_unsupported")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := portableRunPolicy(test.platform, test.action); got != test.want {
				t.Fatalf("portableRunPolicy(%q, %q) = %v, want %v", test.platform, test.action, got, test.want)
			}
		})
	}
}

func TestPluginRunAllowsOwnerScopedRouting(t *testing.T) {
	if !pluginActionAllowed("routing") {
		t.Fatal("portable routing entry was not allowlisted")
	}
}

func TestLinuxPortableTaskFollowupPolicy(t *testing.T) {
	for _, action := range []string{"inspect", "status", "collect", "wait"} {
		t.Run(action, func(t *testing.T) {
			if err := portableRunPolicy("linux", "task", action, "--handle", "/private/handle"); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, args := range [][]string{nil, {"task"}, {"task", "run"}, {"task", "submit"}, {"task", "dispatch"}, {"task", "resume"}, {"task", "answer"}, {"task", "ack"}, {"task", "rework"}, {"task", "stop"}, {"task", "--", "status"}, {"task", "--handle", "status"}, {"Task", "status"}} {
		if err := portableRunPolicy("linux", args...); err != codeError("portable_action_unsupported") {
			t.Fatalf("%q admitted: %v", args, err)
		}
	}
}
