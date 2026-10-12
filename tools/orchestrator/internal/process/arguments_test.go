package process

import (
	"encoding/binary"
	"testing"
)

func TestProcessArgumentDecodeStopsBeforeEnvironment(t *testing.T) {
	body := make([]byte, 4)
	binary.NativeEndian.PutUint32(body, 3)
	body = append(body, []byte("/bin/codex\x00\x00/bin/codex\x00app-server\x00daemon\x00SECRET=not-an-argument\x00")...)
	args, err := decodeProcessArguments(body)
	if err != nil || len(args) != 3 || args[2] != "daemon" {
		t.Fatal(args, err)
	}
	for _, b := range [][]byte{nil, body[:6], body[:len(body)-40], append([]byte{255, 255, 255, 127}, body[4:]...)} {
		if _, err := decodeProcessArguments(b); err == nil {
			t.Fatal("malformed/truncated argv accepted")
		}
	}
}
