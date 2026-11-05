package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/instance"
)

const mcpConfigMaxBytes = 64 * 1024
const mcpTokenMaxBytes = 8192

type mcpConfigureRequest struct {
	Version   int               `json:"version"`
	Root      string            `json:"root"`
	StateDir  string            `json:"state_dir"`
	Workspace string            `json:"workspace"`
	Providers map[string]string `json:"providers"`
}

type mcpConfiguration struct {
	Version   int               `json:"version"`
	Namespace string            `json:"namespace"`
	StateDir  string            `json:"state_dir"`
	Workspace string            `json:"workspace"`
	Providers map[string]string `json:"providers"`
}

// Configuration contains task routing/policy, never owner or control authority.
// A logical owner is observed by the existing dispatch path, not at setup time.
func mcpEntry(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("config", "", "private task-local configuration")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" {
			return codeError("invalid_args")
		}
		return serveMCP(ctx, *path, os.Stdin, out)
	case "configure":
		fs := flag.NewFlagSet("mcp configure", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		request := fs.String("request", "", "private task-local configuration request")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *request == "" {
			return codeError("invalid_args")
		}
		return configureMCP(*request, out)
	default:
		return codeError("invalid_args")
	}
}

func mcpPrivateBytes(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, codeError("mcp_private_file_untrusted")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, codeError("mcp_private_file_untrusted")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || !owned(before) || before.Size() < 1 || before.Size() > limit {
		return nil, codeError("mcp_private_file_untrusted")
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return nil, codeError("mcp_private_file_untrusted")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) != before.Size() {
		return nil, codeError("mcp_private_file_untrusted")
	}
	after, err := f.Stat()
	named, nameErr := os.Lstat(path)
	if err != nil || nameErr != nil || !os.SameFile(before, after) || !os.SameFile(before, named) || before.Size() != after.Size() || before.Mode() != after.Mode() || before.ModTime() != after.ModTime() {
		return nil, codeError("mcp_private_file_untrusted")
	}
	return data, nil
}
func mcpReadPrivate(path string, limit int64, target any) error {
	data, err := mcpPrivateBytes(path, limit)
	if err != nil {
		return err
	}
	return decode(data, target)
}

func mcpCanonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", codeError("mcp_directory_invalid")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", codeError("mcp_directory_invalid")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", codeError("mcp_directory_invalid")
	}
	return canonical, nil
}

func configureMCP(path string, out io.Writer) error {
	var request mcpConfigureRequest
	if err := mcpReadPrivate(path, mcpConfigMaxBytes, &request); err != nil {
		return err
	}
	if request.Version != 1 || !filepath.IsAbs(request.Root) || filepath.Clean(request.Root) != request.Root || !filepath.IsAbs(request.StateDir) || filepath.Clean(request.StateDir) != request.StateDir || len(request.Providers) > 4 {
		return codeError("mcp_config_invalid")
	}
	workspace, err := mcpCanonicalDirectory(request.Workspace)
	if err != nil {
		return err
	}
	if err = instance.PrepareStateDir(request.StateDir); err != nil {
		return err
	}
	state, err := mcpCanonicalDirectory(request.StateDir)
	if err != nil {
		return err
	}
	providers := map[string]string{}
	for name, path := range request.Providers {
		provider, canonicalName, err := taskProvider(name)
		if err != nil || name != canonicalName {
			return codeError("mcp_provider_invalid")
		}
		var lock adapter.ProviderLock
		if err = mcpReadPrivate(path, mcpConfigMaxBytes, &lock); err != nil {
			return err
		}
		if lock.Version != 1 || lock.Provider != provider || lock.Protocol != adapter.ProtocolID(provider) || !filepath.IsAbs(lock.Binary.Path) {
			return codeError("provider_lock_invalid")
		}
		providers[name] = path
	}
	parent, err := mcpCanonicalDirectory(filepath.Dir(request.Root))
	if err != nil {
		return err
	}
	root := filepath.Join(parent, filepath.Base(request.Root))
	if err = os.Mkdir(root, 0700); err != nil {
		return codeError("mcp_config_exists")
	}
	var namespace [16]byte
	var key [32]byte
	if _, err = rand.Read(namespace[:]); err != nil {
		return err
	}
	if _, err = rand.Read(key[:]); err != nil {
		return err
	}
	config := mcpConfiguration{Version: 1, Namespace: hex.EncodeToString(namespace[:]), StateDir: state, Workspace: workspace, Providers: providers}
	if err = writeExclusiveJSON(filepath.Join(root, "config.json"), config); err != nil {
		return err
	}
	// Token authority stays below the existing runtime root denied to workers.
	keyBase := filepath.Join(state, "mcp-keys")
	if err = os.Mkdir(keyBase, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err = privateHandleParent(filepath.Join(keyBase, "entry")); err != nil {
		return err
	}
	keyDirectory := filepath.Join(keyBase, config.Namespace)
	if err = os.Mkdir(keyDirectory, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(keyDirectory, "token-key"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(key[:]); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, directory := range []string{keyDirectory, keyBase, state} {
		directoryFile, e := os.Open(directory)
		if e != nil {
			return e
		}
		e = directoryFile.Sync()
		closeErr := directoryFile.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return err
	}
	client := map[string]any{"mcpServers": map[string]any{"agent_bird": map[string]any{"command": executable, "args": []string{"mcp", "serve", "--config", filepath.Join(root, "config.json")}}}}
	if err = writeExclusiveJSON(filepath.Join(root, "mcp.json"), client); err != nil {
		return err
	}
	commandJSON, _ := json.Marshal(executable)
	argsJSON, _ := json.Marshal([]string{"mcp", "serve", "--config", filepath.Join(root, "config.json")})
	// Forward names only. New auth-sensitive names require reconfiguration;
	// missing forwarding is detected by the managed source-environment proof.
	_, envNames := sourceAuthEnvironmentProjection(os.Environ())
	envNames = append(envNames, managedOwnerRequestEnv)
	sort.Strings(envNames)
	envJSON, _ := json.Marshal(envNames)
	codexArgs := []string{"-c", "mcp_servers.agent_bird.command=" + string(commandJSON), "-c", "mcp_servers.agent_bird.args=" + string(argsJSON), "-c", "mcp_servers.agent_bird.env_vars=" + string(envJSON)}
	if err = writeExclusiveJSON(filepath.Join(root, "codex-args.json"), codexArgs); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "configured", "config": filepath.Join(root, "config.json"), "client_config": filepath.Join(root, "mcp.json"), "scope": "managed_process", "delivery_mode": "collect", "tasks": false, "native_push": false})
}

func loadMCPConfiguration(path string) (mcpConfiguration, []byte, error) {
	var config mcpConfiguration
	if err := privateHandleParent(path); err != nil {
		return config, nil, err
	}
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || canonical != filepath.Dir(path) {
		return config, nil, codeError("mcp_private_file_untrusted")
	}
	if err = mcpReadPrivate(path, mcpConfigMaxBytes, &config); err != nil {
		return config, nil, err
	}
	namespace, err := hex.DecodeString(config.Namespace)
	if err != nil || len(namespace) != 16 || config.Version != 1 || len(config.Providers) > 4 {
		return config, nil, codeError("mcp_config_invalid")
	}
	state, err := mcpCanonicalDirectory(config.StateDir)
	if err != nil || state != config.StateDir {
		return config, nil, codeError("mcp_config_invalid")
	}
	if err = privateHandleParent(filepath.Join(state, "capability")); err != nil {
		return config, nil, err
	}
	if !filepath.IsAbs(config.Workspace) || filepath.Clean(config.Workspace) != config.Workspace {
		return config, nil, codeError("mcp_config_invalid")
	}
	for name, path := range config.Providers {
		_, canonicalName, e := taskProvider(name)
		if e != nil || name != canonicalName || !filepath.IsAbs(path) {
			return config, nil, codeError("mcp_provider_invalid")
		}
	}
	keyDirectory := filepath.Join(config.StateDir, "mcp-keys", config.Namespace)
	if err = privateHandleParent(filepath.Join(keyDirectory, "token-key")); err != nil {
		return config, nil, err
	}
	canonical, err = filepath.EvalSymlinks(keyDirectory)
	if err != nil || canonical != keyDirectory {
		return config, nil, codeError("mcp_private_file_untrusted")
	}
	keyPath := filepath.Join(keyDirectory, "token-key")
	key, err := mcpPrivateBytes(keyPath, 32)
	if err != nil || len(key) != 32 {
		return config, nil, codeError("mcp_private_file_untrusted")
	}
	return config, key, nil
}
