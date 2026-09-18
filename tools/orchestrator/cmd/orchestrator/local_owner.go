package main

import (
	"context"
	"flag"
	"io"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ownerBind(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("owner-bind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	path := fs.String("request", "", "owner identity request")
	currentLocal := fs.Bool("current", false, "observe this managed controller process scope")
	current := fs.Bool("current-codex", false, "observe the calling native Codex session")
	if fs.Parse(args) != nil || (*path == "" && !*current && !*currentLocal) || (*path != "" && (*current || *currentLocal)) || (*current && *currentLocal) || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	var req coordinator.OwnerBindRequest
	var err error
	if *current {
		req, err = currentCodexOwner(ctx)
	} else if *currentLocal {
		req, err = currentOwner(ctx)
	} else {
		err = readPrivateJSON(*path, &req)
	}
	if err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if _, err = ensureServer(ctx, state); err != nil {
		return err
	}
	response, err := call(ctx, state, ipc.KindOwnerBind, req)
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
func verifyLocalProjection(request coordinator.SubmitRequest) error {
	var g store.OwnerGrant
	if err := readPrivateJSON(request.OwnerCapability, &g); err != nil {
		return codeError("owner_capability_invalid")
	}
	if g.Version != 1 || g.Kind != "local-owner" || g.ControllerThread != request.ControllerThread || g.OriginContextID != request.OriginContextID || g.OriginPID != request.OriginPID || g.OriginBirth != request.OriginBirth || g.HostGeneration != request.HostGeneration {
		return codeError("owner_capability_mismatch")
	}
	// Coordinator independently validates the capability and the kernel peer.
	return nil
}

func rebindLocalOwner(ctx context.Context, stateArg, path string, out io.Writer) error {
	var req struct {
		Version         int    `json:"version"`
		OwnerMode       string `json:"owner_mode"`
		OwnerCapability string `json:"owner_capability"`
		ControlFile     string `json:"control_file"`
	}
	if err := readPrivateJSON(path, &req); err != nil {
		return err
	}
	if req.Version != 1 || req.OwnerMode != "local" {
		return codeError("invalid_rebind")
	}
	var owner store.OwnerGrant
	if err := readPrivateJSON(req.OwnerCapability, &owner); err != nil {
		return codeError("owner_capability_invalid")
	}
	capability, err := loadControlCapability(req.ControlFile)
	if err != nil {
		return err
	}
	if capability.ControllerThread != owner.ControllerThread {
		return codeError("owner_mismatch")
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		return err
	}
	if bootstrap.Version != 2 {
		return codeError("legacy_bootstrap_rebind_unsupported")
	}
	state, err := resolveState(stateArg)
	if err != nil {
		return err
	}
	request := coordinator.RebindOwnerRequest{RunID: capability.RunID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, OriginContextID: bootstrap.Hello.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration, OwnerMode: "local", OwnerCapability: req.OwnerCapability}
	response, err := call(ctx, state, ipc.KindRebindOwner, request)
	if err != nil {
		return err
	}
	bootstrap.Hello.OriginPID, bootstrap.Hello.OriginBirth, bootstrap.Hello.HostGeneration = owner.OriginPID, owner.OriginBirth, owner.HostGeneration
	if err = replacePrivateJSON(capability.BootstrapPath, bootstrapMetadata(bootstrap)); err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}
