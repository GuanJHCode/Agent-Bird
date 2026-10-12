package process

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"unicode/utf8"
)

// ArgumentSnapshot is a bounded observation, not an ownership capability.
// Args excludes the environment; callers must bind it to verified ancestry.
type ArgumentSnapshot struct {
	PID, ParentPID, UID int
	Birth, Executable   string
	Args                []string
}

func decodeProcessArguments(body []byte) ([]string, error) {
	if len(body) < 5 || len(body) > 1<<20 {
		return nil, CodeError("process_arguments_unknown")
	}
	argc := int(binary.NativeEndian.Uint32(body[:4]))
	if argc < 1 || argc > 256 {
		return nil, CodeError("process_arguments_unknown")
	}
	body = body[4:]
	end := bytes.IndexByte(body, 0)
	if end < 1 || end > 4096 || !filepath.IsAbs(string(body[:end])) {
		return nil, CodeError("process_arguments_unknown")
	}
	body = body[end+1:]
	for len(body) > 0 && body[0] == 0 {
		body = body[1:]
	}
	args := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(body, 0)
		if end < 0 || end > 8192 || !utf8.Valid(body[:end]) {
			return nil, CodeError("process_arguments_unknown")
		}
		args = append(args, string(body[:end]))
		body = body[end+1:]
	}
	return args, nil
}
