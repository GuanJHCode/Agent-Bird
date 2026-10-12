package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

const bridgeMaxRequestFile = 64 * 1024

var bridgeCLIErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func bridgeCleanError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codeError("bridge_timeout")
	}
	if errors.Is(err, context.Canceled) {
		return codeError("bridge_cancelled")
	}
	if bridgeCLIErrorCode.MatchString(err.Error()) {
		return codeError(err.Error())
	}
	return codeError("bridge_command_failed")
}

func readBridgeRequestFile(path string) ([]byte, error) {
	invalid := codeError("bridge_request_file_invalid")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, invalid
	}
	if canonical, err := filepath.EvalSymlinks(path); err != nil || canonical != path {
		return nil, invalid
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() <= 0 || before.Size() > bridgeMaxRequestFile {
		return nil, invalid
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, invalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > bridgeMaxRequestFile {
		return nil, invalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, invalid
	}
	raw := make([]byte, info.Size())
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, info.Size()), raw); err != nil {
		return nil, invalid
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Mode().Perm() != 0600 {
		return nil, invalid
	}
	return raw, nil
}

func bridgeEntry(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	if args[0] == "capabilities" {
		return bridgeCapabilities(ctx, args[1:], stdout)
	}
	if args[0] == "hook" {
		fs := flag.NewFlagSet("bridge hook", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		provider := fs.String("provider", "", "registered provider")
		nativeHook := fs.String("native-hook", "", "AGY typed event selector")
		binding := fs.String("binding-file", "", "private binding capability")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *provider == "" || (*binding == "" && *provider != "agy") {
			return codeError("invalid_args")
		}
		selectorSupplied := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "native-hook" {
				selectorSupplied = true
			}
		})
		if selectorSupplied || *provider == "agy" {
			var err error
			ctx, err = hookcli.WithNativeHook(ctx, *provider, hookcli.NativeHookSelector(*nativeHook))
			if err != nil {
				return bridgeCleanError(err)
			}
		}
		return hookcli.Run(ctx, *provider, *binding, os.Stdin, stdout, stderr)
	}
	kind, ok := bridgeOwnerKind(args[0])
	if !ok {
		return codeError("invalid_args")
	}
	path, err := requestFile(args[1:])
	if err != nil {
		return err
	}
	raw, err := readBridgeRequestFile(path)
	if err != nil {
		return bridgeCleanError(err)
	}
	request, controlFile, err := decodeBridgeOwnerRequest(kind, raw)
	if err != nil {
		return bridgeCleanError(err)
	}
	if _, err := currentOwner(ctx); err != nil {
		return codeError("current_owner_unavailable")
	}
	if filepath.Base(filepath.Dir(controlFile)) != "control" {
		return codeError("control_file_invalid")
	}
	state := filepath.Dir(filepath.Dir(controlFile))
	response, err := call(ctx, state, kind, request)
	if err != nil {
		return bridgeCleanError(err)
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return bridgeCleanError(err)
}

func bridgeOwnerKind(name string) (ipc.Kind, bool) {
	switch name {
	case "bind":
		return ipc.KindBridgeBind, true
	case "activate":
		return ipc.KindBridgeActivate, true
	case "received":
		return ipc.KindBridgeReceived, true
	case "batch":
		return ipc.KindBridgeQueryBatch, true
	case "redeliver":
		return ipc.KindBridgeRedeliver, true
	case "status":
		return ipc.KindBridgeStatus, true
	case "unbind":
		return ipc.KindBridgeUnbind, true
	}
	return "", false
}

func decodeBridgeOwnerRequest(kind ipc.Kind, raw json.RawMessage) (any, string, error) {
	switch kind {
	case ipc.KindBridgeBind:
		req, err := contract.DecodeBridgeBindRequest(raw)
		return req, req.ControlFile, err
	case ipc.KindBridgeActivate:
		req, err := contract.DecodeBridgeActivateRequest(raw)
		return req, req.ControlFile, err
	case ipc.KindBridgeReceived, ipc.KindBridgeQueryBatch:
		req, err := contract.DecodeBridgeReceiveRequest(raw)
		return req, req.ControlFile, err
	case ipc.KindBridgeRedeliver:
		req, err := contract.DecodeBridgeRedeliverRequest(raw)
		return req, req.ControlFile, err
	case ipc.KindBridgeStatus:
		req, err := contract.DecodeBridgeStatusRequest(raw)
		return req, req.ControlFile, err
	case ipc.KindBridgeUnbind:
		req, err := contract.DecodeBridgeUnbindRequest(raw)
		return req, req.ControlFile, err
	}
	return nil, "", codeError("invalid_args")
}
