package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	directory, nonce, ok := arguments(args)
	if !ok {
		fmt.Fprintln(stderr, "g0-hook: invalid_arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	input, category := readInput(ctx, stdin)
	if category == "" {
		var event map[string]any
		event, category = identity(input)
		if category == "" {
			event["nonce"] = nonce
			event["pid"], event["ppid"] = os.Getpid(), os.Getppid()
			event["recorded_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			if ctx.Err() != nil {
				category = "timeout"
			} else if err := record(directory, event); err != nil {
				category = "io_error"
			}
		}
	}
	if category != "" {
		fmt.Fprintln(stderr, "g0-hook: "+category)
		return 1
	}
	// No stdout, including on success: no model-visible context is injected.
	return 0
}

func arguments(args []string) (directory, nonce string, ok bool) {
	if len(args) != 4 {
		return "", "", false
	}
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "--output-dir":
			if directory != "" {
				return "", "", false
			}
			directory = args[i+1]
		case "--nonce":
			if nonce != "" {
				return "", "", false
			}
			nonce = args[i+1]
		default:
			return "", "", false
		}
	}
	return directory, nonce, len(directory) <= 4096 && filepath.IsAbs(directory) && filepath.Clean(directory) == directory && !strings.ContainsAny(directory, "\x00\r\n") && validID(nonce)
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func readInput(ctx context.Context, input io.Reader) ([]byte, string) {
	type outcome struct {
		data []byte
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, 65537))
		finished <- outcome{data, err}
	}()
	select {
	case <-ctx.Done():
		// The executable always supplies *os.File; closing it releases a
		// blocked stdin read. Finite in-memory readers are used by tests.
		if closer, ok := input.(io.Closer); ok {
			closer.Close()
		}
		return nil, "timeout"
	case result := <-finished:
		if ctx.Err() != nil {
			return nil, "timeout"
		}
		if len(result.data) > 65536 {
			return nil, "input_limit"
		}
		if result.err != nil {
			return nil, "invalid_input"
		}
		return result.data, ""
	}
}

func identity(data []byte) (map[string]any, string) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, "invalid_input"
	}
	seen := map[string]bool{}
	fields := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, "invalid_input"
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, "invalid_input"
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, "invalid_input"
		}
		switch key {
		case "session_id", "hook_event_name", "turn_id", "source":
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, "invalid_input"
			}
			fields[key] = text
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, "invalid_input"
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, "invalid_input"
	}
	if !validID(fields["session_id"]) || fields["hook_event_name"] == "" {
		return nil, "invalid_input"
	}
	event := fields["hook_event_name"]
	switch event {
	case "SessionStart", "UserPromptSubmit", "SessionEnd":
	default:
		return nil, "unsupported_event"
	}
	result := map[string]any{"session_id": fields["session_id"], "hook_event_name": event}
	if turn, present := fields["turn_id"]; present {
		if !validID(turn) {
			return nil, "invalid_input"
		}
		result["turn_id"] = turn
	} else if event == "UserPromptSubmit" {
		return nil, "invalid_input"
	}
	if source, present := fields["source"]; present {
		switch source {
		case "startup", "resume", "clear", "compact":
		default:
			return nil, "invalid_input"
		}
		result["source"] = source
	} else if event == "SessionStart" {
		return nil, "invalid_input"
	}
	return result, ""
}

// Each opened component must have the identity observed through its parent
// handle. No directory is created and static symlinks are rejected.
func outputRoot(directory string) (*os.Root, error) {
	volume := filepath.VolumeName(directory)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(directory, volume), string(filepath.Separator))
	if relative == "" {
		return root, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		before, err := root.Lstat(component)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errors.New("invalid directory")
		}
		next, err := root.OpenRoot(component)
		root.Close()
		if err != nil {
			return nil, err
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			return nil, errors.New("directory identity changed")
		}
		root = next
	}
	return root, nil
}

func record(directory string, event map[string]any) error {
	root, err := outputRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	name := hex.EncodeToString(id[:]) + ".json"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	owned, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	succeeded := false
	defer func() {
		file.Close()
		if !succeeded {
			if current, err := root.Lstat(name); err == nil && os.SameFile(owned, current) {
				root.Remove(name)
			}
		}
	}()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if n, err := file.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	succeeded = true
	return nil
}
