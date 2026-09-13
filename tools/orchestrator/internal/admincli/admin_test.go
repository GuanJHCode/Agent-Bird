package admincli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorRequestFileBridge(t *testing.T) {
	root := t.TempDir()
	requestPath := filepath.Join(root, "doctor.json")
	body, _ := json.Marshal(DoctorRequest{DestinationRoot: filepath.Join(root, "not-installed")})
	if err := os.WriteFile(requestPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run("doctor", requestPath, &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Status != "ok" {
		t.Fatalf("response=%q err=%v", output.String(), err)
	}
}
