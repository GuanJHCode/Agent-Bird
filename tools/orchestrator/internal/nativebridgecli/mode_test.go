package nativebridgecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestB06OldDriverRejectsHookPullBeforeLaunch(t *testing.T) {
	path, req := driverRequestFixture(t)
	req.Mode = "hook_pull"
	privateJSON(t, path, req)
	if _, err := loadDriverRequest(path); err == nil {
		t.Fatal("old native driver accepted hook_pull")
	}
	if err := RunDriver(context.Background(), path, nil, nil); err == nil {
		t.Fatal("hook_pull launched native driver")
	}
	for _, name := range []string{"launcher.json", "status.json"} {
		if _, err := os.Stat(filepath.Join(req.DriverRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("rejected mode created %s", name)
		}
	}
}
