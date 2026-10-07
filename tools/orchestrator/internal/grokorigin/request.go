//go:build !windows

// Package grokorigin owns explicit, private input for the controlled ACP origin.
package grokorigin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const maxRequestBytes = 64 << 10

var errInvalidRequest = errors.New("grokorigin_invalid_request")

type Request struct {
	SchemaVersion      int    `json:"schema_version"`
	Binary             string `json:"binary"`
	BinarySHA256       string `json:"binary_sha256"`
	BinaryVersion      string `json:"binary_version"`
	Workspace          string `json:"workspace"`
	InitialPrompt      string `json:"initial_prompt"`
	OperationTimeoutMS int64  `json:"operation_timeout_ms"`
	TotalTimeoutMS     int64  `json:"total_timeout_ms"`
	OriginProfile      string `json:"origin_profile"`
	AuthMode           string `json:"auth_mode"`
}

func requestText(s string, maximum int) bool {
	if s == "" || len(s) > maximum || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validPath(s string) bool {
	return requestText(s, 4096) && filepath.IsAbs(s) && filepath.Clean(s) == s
}
func (r Request) Validate() error {
	if r.SchemaVersion != 1 || !validPath(r.Binary) || !validPath(r.Workspace) || len(r.BinarySHA256) != 64 || r.BinarySHA256 != strings.ToLower(r.BinarySHA256) || !requestText(r.BinaryVersion, 256) || !utf8.ValidString(r.InitialPrompt) || len(r.InitialPrompt) == 0 || len(r.InitialPrompt) > 8192 || r.OperationTimeoutMS < 100 || r.OperationTimeoutMS > 300000 || r.TotalTimeoutMS < r.OperationTimeoutMS || r.TotalTimeoutMS > 3600000 || r.OriginProfile != "readonly" || (r.AuthMode != "none" && r.AuthMode != "xai_api_key_env") {
		return errInvalidRequest
	}
	if _, err := hex.DecodeString(r.BinarySHA256); err != nil {
		return errInvalidRequest
	}
	return nil
}

// encoding/json accepts malformed UTF-16 escapes by replacing them. Reject
// those escapes before decoding, while preserving legitimate U+FFFD text.
func requestUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		var code uint16
		for _, b := range raw[i+1 : i+5] {
			code <<= 4
			switch {
			case b >= '0' && b <= '9':
				code += uint16(b - '0')
			case b >= 'a' && b <= 'f':
				code += uint16(b - 'a' + 10)
			case b >= 'A' && b <= 'F':
				code += uint16(b - 'A' + 10)
			default:
				return false
			}
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			second, err := hex.DecodeString(string(raw[i+3 : i+7]))
			if err != nil {
				return false
			}
			low := uint16(second[0])<<8 | uint16(second[1])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func decodeRequest(raw []byte) (Request, error) {
	var zero Request
	if len(raw) > maxRequestBytes || !requestUnicode(raw) {
		return zero, errInvalidRequest
	}
	required := map[string]bool{"schema_version": false, "binary": false, "binary_sha256": false, "binary_version": false, "workspace": false, "initial_prompt": false, "operation_timeout_ms": false, "total_timeout_ms": false, "origin_profile": false, "auth_mode": false}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return zero, errInvalidRequest
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return zero, errInvalidRequest
		}
		name, ok := key.(string)
		seen, known := required[name]
		if !ok || !known || seen {
			return zero, errInvalidRequest
		}
		required[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return zero, errInvalidRequest
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return zero, errInvalidRequest
	}
	if _, err = d.Token(); err != io.EOF {
		return zero, errInvalidRequest
	}
	for _, seen := range required {
		if !seen {
			return zero, errInvalidRequest
		}
	}
	var r Request
	if json.Unmarshal(raw, &r) != nil || r.Validate() != nil {
		return zero, errInvalidRequest
	}
	return r, nil
}

func privateRequestFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode() != 0600 || info.Size() < 0 || info.Size() > maxRequestBytes {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid() && st.Nlink == 1
}
func sameRequestFile(before, after os.FileInfo) bool {
	return privateRequestFile(before) && privateRequestFile(after) && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func ReadRequest(path string) (Request, error) { return readRequest(path, nil) }

// A per-call observation seam permits deterministic swap tests without global
// process state or weakening any file check. Production always passes nil.
func readRequest(path string, observe func(string) error) (Request, error) {
	var zero Request
	if !validPath(path) {
		return zero, errInvalidRequest
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return zero, errInvalidRequest
	}
	before, err := os.Lstat(path)
	if err != nil || !privateRequestFile(before) {
		return zero, errInvalidRequest
	}
	if observe != nil && observe("before_open") != nil {
		return zero, errInvalidRequest
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return zero, errInvalidRequest
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameRequestFile(before, opened) {
		return zero, errInvalidRequest
	}
	if observe != nil && observe("before_read") != nil {
		return zero, errInvalidRequest
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRequestBytes+1))
	if err != nil || int64(len(raw)) != before.Size() {
		return zero, errInvalidRequest
	}
	if observe != nil && observe("after_read") != nil {
		return zero, errInvalidRequest
	}
	after, err := f.Stat()
	if err != nil || !sameRequestFile(before, after) {
		return zero, errInvalidRequest
	}
	current, err := os.Lstat(path)
	if err != nil || !sameRequestFile(before, current) {
		return zero, errInvalidRequest
	}
	canonical, err = filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return zero, errInvalidRequest
	}
	return decodeRequest(raw)
}
