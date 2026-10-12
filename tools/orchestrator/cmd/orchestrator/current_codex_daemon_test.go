package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCurrentCodexDaemonPairSelection(t *testing.T) {
	for _, variant := range []string{"valid", "reversed", "single", "third", "parent", "daemon_parent", "path", "digest", "birth", "uid", "argv", "daemon_argv", "read_error", "unknown_flag"} {
		t.Run(variant, func(t *testing.T) {
			path := "/private/codex"
			origins := []codexOrigin{{coordinator.OwnerBindRequest{OriginPID: 20, OriginBirth: "child"}, path, "digest"}, {coordinator.OwnerBindRequest{OriginPID: 10, OriginBirth: "daemon"}, path, "digest"}}
			snapshots := map[int]process.ArgumentSnapshot{
				20: {PID: 20, ParentPID: 10, UID: os.Geteuid(), Birth: "child", Executable: path, Args: []string{path, "app-server", "--remote-control", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"}},
				10: {PID: 10, ParentPID: 1, UID: os.Geteuid(), Birth: "daemon", Executable: path, Args: []string{path, "app-server", "daemon", "pid-update-loop"}},
			}
			child, daemon := snapshots[20], snapshots[10]
			switch variant {
			case "reversed":
				origins[0], origins[1] = origins[1], origins[0]
			case "single":
				origins = origins[:1]
			case "third":
				origins = append(origins, origins[0])
			case "parent":
				child.ParentPID = 9
			case "daemon_parent":
				daemon.ParentPID = 3
			case "path":
				origins[1].path = "/other/codex"
			case "digest":
				origins[1].digest = "different"
			case "birth":
				child.Birth = "changed"
			case "uid":
				child.UID++
			case "argv":
				child.Args = []string{path, "exec"}
			case "daemon_argv":
				daemon.Args = []string{path, "app-server", "daemon", "other"}
			case "unknown_flag":
				child.Args = append(child.Args, "--unknown")
			}
			snapshots[20], snapshots[10] = child, daemon
			calls := 0
			result, err := selectCodexOrigin(context.Background(), origins, func(_ context.Context, pid int) (process.ArgumentSnapshot, error) {
				calls++
				if variant == "read_error" {
					return process.ArgumentSnapshot{}, errors.New("unavailable")
				}
				return snapshots[pid], nil
			})
			good := variant == "valid" || variant == "reversed" || variant == "single"
			if good && (err != nil || result.OriginPID != 20) {
				t.Fatal(result, err)
			}
			if !good && err == nil {
				t.Fatal("ambiguous origin accepted")
			}
			if variant == "single" && calls != 0 {
				t.Fatal("ordinary CLI acquired new argv dependency")
			}
		})
	}
}
