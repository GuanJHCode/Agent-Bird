package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const bridgeHandleEnv = "AGENT_BIRD_BRIDGE_HANDLE"
const bridgeBindingEnv = "AGENT_BIRD_BRIDGE_BINDING"

type controllerOwnerGrant struct {
	Version          int    `json:"version"`
	OwnerMode        string `json:"owner_mode"`
	OwnerCapability  string `json:"owner_capability"`
	ControllerThread string `json:"controller_thread"`
	OriginContextID  string `json:"origin_context_id"`
	OriginPID        int    `json:"origin_pid"`
	OriginBirth      string `json:"origin_birth"`
	HostGeneration   string `json:"host_generation"`
}

func readControllerBridgePlan(planPath, handle string) (taskPlan, error) {
	var plan taskPlan
	raw, err := readBridgeRequestFile(planPath)
	if err != nil {
		return plan, err
	}
	if err = decode(raw, &plan); err != nil {
		return plan, codeError("invalid_task_plan")
	}
	if plan.RunID == "" || strings.TrimSpace(plan.RunID) != plan.RunID || plan.PlanRevision < 1 || len(plan.Tasks) == 0 {
		return plan, codeError("invalid_task_plan")
	}
	if err = privateHandleParent(handle); err != nil {
		return plan, err
	}
	if _, err = os.Lstat(handle); err == nil {
		return plan, codeError("task_handle_exists")
	} else if !os.IsNotExist(err) {
		return plan, err
	}
	if _, err := os.Lstat(handle + ".bridge-owner.json"); err == nil {
		return plan, codeError("controller_bridge_metadata_exists")
	} else if !os.IsNotExist(err) {
		return plan, err
	}
	ids := []string{}
	for _, task := range plan.Tasks {
		if task.ID == "" || slices.Contains(ids, task.ID) || task.MaxAttempts < 1 || task.MaxActiveMS < 1 {
			return plan, codeError("invalid_task_plan")
		}
		ids = append(ids, task.ID)
	}
	return plan, nil
}

// The first actual OwnerBind grant is reused. This function never calls
// taskSubmitPrepared/OwnerBind and never adopts an existing run or reservation.
func submitControllerBridge(ctx context.Context, state, handle string, plan taskPlan, owner controllerOwnerGrant, provider string) (taskHandle, error) {
	if hookcli.ProviderManifestSHA256(provider) == "" {
		return taskHandle{}, codeError("bridge_provider_unknown")
	}
	h := taskHandle{Version: 1, RunID: plan.RunID, StateDir: state, Status: "submitting", TaskIDs: []string{}}
	for _, task := range plan.Tasks {
		h.TaskIDs = append(h.TaskIDs, task.ID)
	}
	if err := requireTaskCoordinator(ctx, state, plan.Tasks); err != nil {
		return h, err
	}
	if err := requireProviderCoordinator(ctx, state); err != nil {
		return h, err
	}
	prepared, err := prepareTaskPlanForOwner(ctx, state, handle, plan, owner, configPreparationOptions{})
	if err != nil {
		return h, err
	}
	plan = prepared.ResolvedPlan
	if err := writeControllerBridgeExclusive(handle, h); err != nil {
		if errors.Is(err, os.ErrExist) {
			return h, codeError("task_handle_exists")
		}
		return h, err
	}
	metadata := controllerBridgeMetadata{Version: 1, RunID: h.RunID, OwnerMode: "local", OwnerCapabilityFile: owner.OwnerCapability, BindingFile: store.BridgeCapabilityPath(state, h.RunID), ManifestSHA256: hookcli.ProviderManifestSHA256(provider)}
	if _, err := readBridgeRequestFile(owner.OwnerCapability); err != nil {
		return h, codeError("controller_owner_receipt_invalid")
	}
	if err := writeControllerBridgeExclusive(handle+".bridge-owner.json", metadata); err != nil {
		return h, err
	}
	req := coordinator.SubmitRequest{RunID: plan.RunID, PlanRevision: plan.PlanRevision, Tasks: plan.Tasks, OwnerMode: "local", DeliveryMode: "collect", OwnerCapability: owner.OwnerCapability, ControllerThread: owner.ControllerThread, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration}
	var receipt bytes.Buffer
	submitErr := withTaskRequest(req, func(path string) error {
		return submitWithPreparedControl(ctx, []string{"--state-dir", state, "--request", path}, &receipt, func(controlPath string) error {
			h.ControlFile = controlPath
			if err := replacePrivateJSON(handle, h); err != nil {
				return err
			}
			if _, err := readBridgeRequestFile(controlPath); err != nil {
				return err
			}
			next := metadata
			next.ControlFile = controlPath
			if err := replaceControllerBridgeMetadata(handle+".bridge-owner.json", metadata, next); err != nil {
				return err
			}
			metadata = next
			return nil
		})
	})
	var rejected remoteError
	if errors.As(submitErr, &rejected) {
		if e := recordRejectedConfigSubmission(handle, &h, prepared); e != nil {
			return h, errors.Join(submitErr, e)
		}
	}
	var r struct {
		Version     int    `json:"version"`
		Status      string `json:"status"`
		RunID       string `json:"run_id"`
		ControlFile string `json:"control_file"`
	}
	if receipt.Len() > 0 {
		if err := decode(receipt.Bytes(), &r); err != nil || r.Version != 1 || r.RunID != h.RunID || r.ControlFile == "" || r.ControlFile != h.ControlFile {
			return h, codeError("task_receipt_invalid")
		}
		// The original task recovery contract accepts its persisted submitting
		// reservation. Uncertain/Host-unavailable receipts are CLI outcomes,
		// never new handle states; preserve the original failure and recovery.
		if r.Status == "queued" {
			h.Status = "queued"
		} else if r.Status != "submit_uncertain" && r.Status != "queued_host_unavailable" {
			return h, codeError("task_receipt_invalid")
		}
		if err := replacePrivateJSON(handle, h); err != nil {
			return h, err
		}
	}
	if submitErr != nil {
		controllerBridgeRecoveryNotice(handle, h)
		return h, submitErr
	}
	if r.Status != "queued" || h.Status != "queued" || h.ControlFile == "" {
		return h, codeError("controller_bridge_not_ready")
	}
	return h, nil
}

func controllerBridgeRecoveryNotice(path string, h taskHandle) {
	if h.ControlFile == "" {
		return
	}
	action := "inspect"
	if h.Status == "submitting" {
		action = "reconcile"
	}
	// File references only; recovery retains the original authorization checks.
	_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "controller_bridge_recovery_required", "handle": path, "next_argv": []string{"task", action, "--handle", path}})
}

func bindControllerBridge(ctx context.Context, state, handlePath string, h taskHandle, owner controllerOwnerGrant, pid int, birth string, provider string) error {
	if hookcli.ProviderManifestSHA256(provider) == "" {
		return codeError("bridge_provider_unknown")
	}
	if now, err := process.Birth(pid); err != nil || now != birth {
		return codeError("controller_process_tree_unknown")
	}
	path, err := process.ExecutablePath(pid)
	if err != nil {
		return codeError("controller_bridge_identity_unverified")
	}
	digest, err := process.ExecutableDigest(ctx, path)
	if err != nil {
		return codeError("controller_bridge_identity_unverified")
	}
	if now, err := process.Birth(pid); err != nil || now != birth {
		return codeError("controller_process_tree_unknown")
	}
	id, err := randomValue("controller-bridge-bind")
	if err != nil {
		return err
	}
	req := contract.BridgeBindRequest{SchemaVersion: 1, RequestID: id, RunID: h.RunID, ProviderID: provider, NativePID: pid, NativeBirth: birth, NativeExecutableSHA256: digest, TransportMode: "hook_pull", OwnerMode: "local", OwnerCapabilityFile: owner.OwnerCapability, ControlFile: h.ControlFile, ManifestSHA256: hookcli.ProviderManifestSHA256(provider)}
	response, err := call(ctx, state, ipc.KindBridgeBind, req)
	if err != nil {
		return bridgeCleanError(err)
	}
	var view contract.BridgeBindingView
	if json.Unmarshal(response.Payload, &view) != nil || view.RunID != h.RunID || view.ProviderID != provider || view.ManifestSHA256 != hookcli.ProviderManifestSHA256(provider) || view.OwnerContextID != owner.OriginContextID || view.OwnerPID != owner.OriginPID || view.OwnerBirth != owner.OriginBirth || view.NativePID != pid || view.NativeBirth != birth || view.NativeExecutableSHA256 != digest || view.Role != "owner" || view.Status != "PENDING_SESSION" || view.CapabilityFile != store.BridgeCapabilityPath(state, h.RunID) {
		return codeError("controller_bridge_receipt_invalid")
	}
	metadataRaw, err := readBridgeRequestFile(handlePath + ".bridge-owner.json")
	if err != nil {
		return err
	}
	var metadata controllerBridgeMetadata
	if err = decode(metadataRaw, &metadata); err != nil {
		return codeError("controller_bridge_metadata_invalid")
	}
	if metadata.RunID != h.RunID || metadata.OwnerCapabilityFile != owner.OwnerCapability || metadata.ControlFile != h.ControlFile || metadata.BindingFile != view.CapabilityFile || metadata.ManifestSHA256 != hookcli.ProviderManifestSHA256(provider) {
		return codeError("controller_bridge_metadata_invalid")
	}
	next := metadata
	next.BindingID = view.BindingID
	next.OwnerEpoch = view.OwnerEpoch
	return replaceControllerBridgeMetadata(handlePath+".bridge-owner.json", metadata, next)
}

func controllerBridgeEnvironment(env []string) []string {
	for _, key := range []string{bridgeBindingEnv, bridgeHandleEnv, "AGENT_BIRD_BRIDGE_RUN_ID", "AGENT_BIRD_BRIDGE_BOOTSTRAP", "AGENT_BIRD_RUN_HANDLE"} {
		env = environmentWithout(env, key)
	}
	return env
}

// Routing-only companion for explicit owner commands. No credential bytes are
// copied; original owner/control files already live in persistent runtime state.
type controllerBridgeMetadata struct {
	Version             int    `json:"version"`
	RunID               string `json:"run_id"`
	OwnerMode           string `json:"owner_mode"`
	OwnerCapabilityFile string `json:"owner_capability_file"`
	ControlFile         string `json:"control_file,omitempty"`
	BindingFile         string `json:"binding_file"`
	BindingID           string `json:"binding_id,omitempty"`
	OwnerEpoch          uint64 `json:"owner_epoch,omitempty"`
	ManifestSHA256      string `json:"manifest_sha256"`
}

func writeControllerBridgeExclusive(path string, value any) error {
	if err := privateHandleParent(path); err != nil {
		return err
	}
	if err := writeExclusiveJSON(path, value); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func replaceControllerBridgeMetadata(path string, expected, next controllerBridgeMetadata) error {
	if err := privateHandleParent(path); err != nil {
		return err
	}
	raw, err := readBridgeRequestFile(path)
	if err != nil {
		return err
	}
	var prior controllerBridgeMetadata
	if err = decode(raw, &prior); err != nil || prior != expected {
		return codeError("controller_bridge_metadata_conflict")
	}
	return replacePrivateJSON(path, next)
}
