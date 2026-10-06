//go:build linux

package process

import (
	"strings"
	"testing"
)

func TestLinuxParseStatAcceptsSpacesAndRightParensInComm(t *testing.T) {
	fields := []string{"S", "7", "8"}
	fields = append(fields, make([]string, 16)...)
	for i := 3; i < len(fields); i++ {
		fields[i] = "0"
	}
	fields = append(fields, "123")
	got, err := linuxParseStat([]byte("42 (a bad) comm) " + strings.Join(fields, " ") + "\n"))
	if err != nil || got.ppid != 7 || got.pgid != 8 || got.state != 'S' || got.start != "123" {
		t.Fatalf("stat=%+v err=%v", got, err)
	}
}

func TestLinuxParseStatRejectsMalformedOrUntrustedValues(t *testing.T) {
	for _, value := range []string{
		"42 no-parens S 7 8 9", "42 (x) S -1 8 9 0 0 0 0 0 0 0 0 0 0 0 0 0 0 123",
		"42 (x) S 7 8 9 0 0 0 0 0 0 0 0 0 0 0 0 0 0 nope",
	} {
		if _, err := linuxParseStat([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
