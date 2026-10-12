package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func codexProtectedLimit(name string) int {
	switch name {
	case "codex-origin.json", "codex-session-seal.json":
		return 16 << 10
	case "codex-thread.json":
		return 8 << 10
	default:
		return 0
	}
}

// Fixed protected metadata only, not a native/schema framework. A duplicate-
// and null-free lossless token tree must round-trip through the concrete record
// with exactly the same keys/types/values. This rejects omitted required members,
// unknown/case-folded aliases and encoding/json's lossy/default substitutions.
func codexDecodeProtectedRecord(body []byte, limit int, out any) error {
	original, err := codexProtectedObject(body, limit)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, out) != nil {
		return errCodexStateFile
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return errCodexStateFile
	}
	normalized, err := codexProtectedObject(encoded, limit)
	if err != nil || !reflect.DeepEqual(original, normalized) {
		return errCodexStateFile
	}
	return nil
}

func codexProtectedObject(body []byte, limit int) (map[string]any, error) {
	if limit < 1 || limit > 16<<10 || len(body) > limit || !utf8.Valid(body) || !codexRecordUnicode(body) {
		return nil, errCodexStateFile
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	work := 4096
	value, err := codexProtectedValue(dec, 0, &work)
	if err != nil {
		return nil, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, errCodexStateFile
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errCodexStateFile
	}
	return object, nil
}

func codexProtectedValue(dec *json.Decoder, depth int, work *int) (any, error) {
	*work--
	if depth > 16 || *work < 0 {
		return nil, errCodexStateFile
	}
	token, err := dec.Token()
	if err != nil || token == nil {
		return nil, errCodexStateFile
	}
	if text, ok := token.(string); ok && len(text) > 4096 {
		return nil, errCodexStateFile
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return nil, errCodexStateFile
			}
			key, ok := token.(string)
			if !ok || len(key) > 128 {
				return nil, errCodexStateFile
			}
			if _, found := object[key]; found {
				return nil, errCodexStateFile
			}
			value, err := codexProtectedValue(dec, depth+1, work)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if token, err := dec.Token(); err != nil || token != json.Delim('}') {
			return nil, errCodexStateFile
		}
		return object, nil
	case '[':
		result := make([]any, 0)
		for dec.More() {
			if len(result) >= 256 {
				return nil, errCodexStateFile
			}
			value, err := codexProtectedValue(dec, depth+1, work)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		if token, err := dec.Token(); err != nil || token != json.Delim(']') {
			return nil, errCodexStateFile
		}
		return result, nil
	default:
		return nil, errCodexStateFile
	}
}

// encoding/json repairs invalid UTF-16 escapes; protected identity must not.
func codexRecordUnicode(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+5 > len(body) {
			return false
		}
		code, ok := codexRecordHex(body[i+1 : i+5])
		if !ok || code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+11 > len(body) || body[i+5] != '\\' || body[i+6] != 'u' {
				return false
			}
			low, ok := codexRecordHex(body[i+7 : i+11])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 10
		} else {
			i += 4
		}
	}
	return true
}
func codexRecordHex(body []byte) (uint16, bool) {
	var value uint16
	for _, b := range body {
		value <<= 4
		switch {
		case b >= '0' && b <= '9':
			value |= uint16(b - '0')
		case b >= 'a' && b <= 'f':
			value |= uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			value |= uint16(b-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

// A failed write leaves evidence in place. There is no adoption/overwrite or
// cleanup of an unproved record; publication is exclusive under anchored0700.
func codexWriteProtectedRecord(ctx context.Context, root codexStateRoot, name string, body []byte) (codexStateFile, error) {
	var empty codexStateFile
	limit := codexProtectedLimit(name)
	if _, err := codexProtectedObject(body, limit); err != nil {
		return empty, err
	}
	chain, err := codexOpenAnchoredStateRoot(ctx, root)
	if err != nil {
		return empty, err
	}
	defer chain.close()
	parent := chain[len(chain)-1].fd
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return empty, errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var initial unix.Stat_t
	if unix.Fstat(fd, &initial) != nil || !codexSafeOwnedFile(initial) || uint32(initial.Mode)&07777 != 0600 || initial.Size != 0 {
		return empty, errCodexStateFile
	}
	for left := body; len(left) > 0; {
		if ctx.Err() != nil {
			return empty, errCodexStateFile
		}
		n, err := f.Write(left)
		if err != nil || n == 0 {
			return empty, errCodexStateFile
		}
		left = left[n:]
	}
	if ctx.Err() != nil || f.Sync() != nil {
		return empty, errCodexStateFile
	}
	var done, current unix.Stat_t
	if unix.Fstat(fd, &done) != nil || !codexSafeOwnedFile(done) || uint32(done.Mode)&07777 != 0600 || done.Size != int64(len(body)) || done.Dev != initial.Dev || done.Ino != initial.Ino || unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || codexFileFromStat(current) != codexFileFromStat(done) || chain.verify(ctx) != nil || unix.Fsync(parent) != nil || chain.verify(ctx) != nil {
		return empty, errCodexStateFile
	}
	return codexStateFile{Present: true, Identity: codexFileFromStat(done), SHA256: codexDigest(body)}, nil
}

func codexReadProtectedRecord(ctx context.Context, root codexStateRoot, name string, out any) (codexStateFile, error) {
	var empty codexStateFile
	limit := codexProtectedLimit(name)
	if limit == 0 {
		return empty, errCodexStateFile
	}
	chain, err := codexOpenAnchoredStateRoot(ctx, root)
	if err != nil {
		return empty, err
	}
	defer chain.close()
	parent := chain[len(chain)-1].fd
	var initial unix.Stat_t
	err = unix.Fstatat(parent, name, &initial, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if chain.verify(ctx) != nil {
			return empty, errCodexStateFile
		}
		return empty, os.ErrNotExist
	}
	if err != nil || !codexSafeOwnedFile(initial) || uint32(initial.Mode)&07777 != 0600 || initial.Size < 1 || initial.Size > int64(limit) {
		return empty, errCodexStateFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return empty, errCodexStateFile
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var opened unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || codexFileFromStat(opened) != codexFileFromStat(initial) {
		return empty, errCodexStateFile
	}
	body := make([]byte, int(initial.Size))
	if _, err := io.ReadFull(f, body); err != nil {
		return empty, errCodexStateFile
	}
	var after, current unix.Stat_t
	if ctx.Err() != nil || unix.Fstat(fd, &after) != nil || codexFileFromStat(after) != codexFileFromStat(initial) || unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || codexFileFromStat(current) != codexFileFromStat(initial) || chain.verify(ctx) != nil || codexDecodeProtectedRecord(body, limit, out) != nil {
		return empty, errCodexStateFile
	}
	return codexStateFile{Present: true, Identity: codexFileFromStat(initial), SHA256: codexDigest(body)}, nil
}
