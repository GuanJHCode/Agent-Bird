package gitops

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestFilterTargetIndexReadOnlyContract(t *testing.T) {
	oid := strings.Repeat("a", 40)
	base := []filterTreeEntry{{path: "a file", mode: "100644", oid: oid, size: 1}}
	good := []byte("H 100644 " + oid + " 0\ta file\x00")
	for _, tc := range []struct {
		name string
		raw  []byte
		ok   bool
	}{
		{"exact", good, true},
		{"staged_blob", []byte("H 100644 " + strings.Repeat("b", 40) + " 0\ta file\x00"), false},
		{"staged_mode", []byte("H 100755 " + oid + " 0\ta file\x00"), false},
		{"staged_path", []byte("H 100644 " + oid + " 0\tanother\x00"), false},
		{"unmerged", []byte("M 100644 " + oid + " 2\ta file\x00"), false},
		{"skip_worktree", []byte("S 100644 " + oid + " 0\ta file\x00"), false},
		{"assume_unchanged", []byte("h 100644 " + oid + " 0\ta file\x00"), false},
		{"extra", append(append([]byte(nil), good...), good...), false},
		{"missing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := filterTargetIndexVector(tc.raw, base); (err == nil) != tc.ok {
				t.Fatalf("index=%v wantOK=%t", err, tc.ok)
			}
		})
	}
	for _, v := range []uint32{1, 2, 3, 4, 5} {
		b := make([]byte, 32)
		copy(b, "DIRC")
		binary.BigEndian.PutUint32(b[4:], v)
		binary.BigEndian.PutUint32(b[8:], 1)
		if err := filterTargetIndexHeader(b); (err == nil) != (v >= 2 && v <= 4) {
			t.Fatalf("version=%d err=%v", v, err)
		}
	}
	b := make([]byte, 32)
	copy(b, "DIRC")
	binary.BigEndian.PutUint32(b[4:], 2)
	binary.BigEndian.PutUint32(b[8:], 4097)
	if filterTargetIndexHeader(b) == nil || filterTargetIndexHeader(make([]byte, 16<<20+1)) == nil {
		t.Fatal("unbounded index accepted")
	}
}
