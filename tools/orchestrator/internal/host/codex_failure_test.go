package host

import (
	"errors"
	"strings"
	"testing"
)

func TestCodexFailureDoesNotExposeNativeError(t *testing.T) {
	body := string(codexFailureContent(errors.New("secret token and private path")))
	if strings.Contains(body, "secret") || body != `{"status":"failed","reason":"codex_managed_protocol_failed"}` {
		t.Fatal(body)
	}
}
