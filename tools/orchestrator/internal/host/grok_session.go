package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func grokSessionID(grant contract.LaunchCommand) string {
	b := sha256.Sum256([]byte("grok-session\x00" + grant.RunID + "\x00" + grant.TaskID + "\x00" + grant.CommandID))
	b[6] = (b[6] & 15) | 0x40
	b[8] = (b[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func grokEncodeCWD(cwd string) string {
	result := ""
	for _, c := range []byte(cwd) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			result += string(c)
		} else {
			result += fmt.Sprintf("%%%02X", c)
		}
	}
	return result
}

func prepareGrokCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, grant contract.LaunchCommand, scratch string) (process.Command, error) {
	if profile == nil || !profile.GrokSessionWrite || profile.Version != 1 || ((profile.Role != adapter.Reviewer || profile.Permission != adapter.ReadOnly) && (profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite)) || grant.CommandID == "" {
		return cmd, errors.New("grok_session_write_required")
	}
	if err := ctx.Err(); err != nil {
		return cmd, err
	}
	if err := grokOwnedDirectory(cmd.Dir, false, false); err != nil {
		return cmd, err
	}
	encoded := grokEncodeCWD(cmd.Dir)
	if len(encoded) > 255 {
		return cmd, errors.New("grok_workspace_path_too_long")
	}
	lookup := func(key string) string {
		value := os.Getenv(key)
		for _, item := range cmd.Env {
			if v, ok := strings.CutPrefix(item, key+"="); ok {
				value = v
			}
		}
		return value
	}
	home := lookup("GROK_HOME")
	if home == "" {
		home = filepath.Join(lookup("HOME"), ".grok")
	}
	if err := grokOwnedDirectory(home, false, false); err != nil {
		return cmd, err
	}
	for _, name := range []string{"config.toml", "managed_config.toml", "requirements.toml", "sandbox.toml", "trusted_folders.toml", "hooks-paths"} {
		info, err := os.Lstat(filepath.Join(home, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return cmd, errors.New("grok_policy_slot_untrusted")
		}
	}
	if err := grokOwnedDirectory(scratch, true, true); err != nil {
		return cmd, err
	}
	sessionID := grokSessionID(grant)
	session := filepath.Join(home, "sessions", encoded, sessionID)
	socketRoot := filepath.Join("/private/tmp", "codex-grok-"+sessionID)
	// Exclusive durable intent is never removed on failure or unknown state.
	f, err := os.OpenFile(filepath.Join(scratch, "grok-session-intent.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return cmd, errors.New("grok_session_intent_exists_or_unwritable")
	}
	writeErr := json.NewEncoder(f).Encode(map[string]string{"session_id": sessionID, "session_directory": session, "socket_directory": socketRoot, "command_id": grant.CommandID})
	syncErr, closeErr := f.Sync(), f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return cmd, err
	}
	intentParent, err := os.Open(scratch)
	if err != nil {
		return cmd, err
	}
	syncErr, closeErr = intentParent.Sync(), intentParent.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return cmd, err
	}
	for _, path := range []string{filepath.Join(home, "sessions"), filepath.Dir(session)} {
		if err := grokOwnedDirectory(path, true, true); err != nil {
			return cmd, err
		}
	}
	if err := os.Mkdir(session, 0700); err != nil {
		return cmd, errors.New("grok_session_directory_exists_or_unwritable")
	}
	canonical, evalErr := filepath.EvalSymlinks("/private/tmp")
	info, statErr := os.Stat("/private/tmp")
	if evalErr != nil || canonical != "/private/tmp" || statErr != nil || !info.IsDir() {
		return cmd, errors.New("grok_socket_parent_untrusted")
	}
	if err := os.Mkdir(socketRoot, 0700); err != nil {
		return cmd, errors.New("grok_socket_directory_exists_or_unwritable")
	}
	cmd.Args = append(cmd.Args, "--session-id", sessionID, "--leader-socket", filepath.Join(socketRoot, "leader.sock"))
	return sandboxCommand(ctx, cmd, profile, scratch, grokSandbox{session: session, socket: socketRoot})
}

type grokSandbox struct{ session, socket string }

func grokOwnedDirectory(path string, create, private bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("grok_directory_untrusted")
	}
	// Validate the existing ancestor before creating descendants; never traverse
	// a pre-existing symlink even during preparatory directory creation.
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || !create {
			return errors.New("grok_directory_untrusted")
		}
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil || resolved != ancestor {
		return errors.New("grok_directory_untrusted")
	}
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	resolved, err = filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("grok_directory_untrusted")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return errors.New("grok_directory_untrusted")
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 || private && info.Mode().Perm()&0077 != 0 {
		return errors.New("grok_directory_permissions")
	}
	return nil
}
