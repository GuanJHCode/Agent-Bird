package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

// Diagnose only the fixed, non-secret paths used by the controlled test worker.
func main() {
	for _, path := range []string{"/bin/sh", "/bin/sleep", "/private/tmp", "/tmp"} {
		info, err := os.Stat(path)
		r := map[string]any{"path": path, "exists": err == nil}
		canonical, canonicalErr := filepath.EvalSymlinks(path)
		r["canonical"] = canonical
		if canonicalErr != nil { r["canonical_error"] = canonicalErr.Error() }
		if err != nil { r["error"] = err.Error() } else { r["mode"] = info.Mode().String() }
		_ = json.NewEncoder(os.Stdout).Encode(r)
	}
	cmd := exec.Command("/bin/sh", "-c", "printf controlled-fixture-probe")
	cmd.Dir = "/tmp"
	body, err := cmd.CombinedOutput()
	r := map[string]any{"shell_output": string(body), "started": err == nil}
	if err != nil { r["error"] = err.Error() }
	_ = json.NewEncoder(os.Stdout).Encode(r)
}
