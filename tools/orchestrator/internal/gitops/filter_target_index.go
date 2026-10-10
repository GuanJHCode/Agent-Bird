package gitops

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// Git remains the index parser. Bound its input before any index-consuming
// command; split-index sidecars are separately rejected by the live observer.
func filterTargetIndexHeader(raw []byte) error {
	if len(raw) < 32 || len(raw) > 16<<20 || string(raw[:4]) != "DIRC" {
		return fail(CodeInvalidInput, "filter target index header", nil)
	}
	version, count := binary.BigEndian.Uint32(raw[4:8]), binary.BigEndian.Uint32(raw[8:12])
	if version < 2 || version > 4 || count > 4096 {
		return fail(CodeInvalidInput, "filter target index bounds", nil)
	}
	return nil
}

// ls-files --stage -v -z --full-name --sparse. Only H (ordinary tracked)
// stage-zero entries are allowed. Compare every path/mode/OID, not cache-tree.
func filterTargetIndexVector(raw []byte, base []filterTreeEntry) error {
	bad := func() error { return fail(CodeTargetDirty, "filter target index differs from base", nil) }
	if len(raw) == 0 || len(raw) > 1<<20 || raw[len(raw)-1] != 0 {
		return bad()
	}
	records := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(records) != len(base) {
		return bad()
	}
	for i, record := range records {
		meta, p, ok := strings.Cut(string(record), "\t")
		fields := strings.Split(meta, " ")
		if !ok || len(fields) != 4 || fields[0] != "H" || fields[1] != base[i].mode || fields[2] != base[i].oid || fields[3] != "0" || p != base[i].path {
			return bad()
		}
	}
	return nil
}
