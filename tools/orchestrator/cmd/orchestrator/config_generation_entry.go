package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"slices"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type configPreparationOptions struct {
	RequestSHA256 string
	Legacy        bool
}

// This entry receives the original successful OwnerBind receipt; it does not
// take a caller-nominated scope or repeat OwnerBind for controller submissions.
func prepareTaskPlanForOwner(ctx context.Context, state, handle string, plan taskPlan, owner controllerOwnerGrant, options configPreparationOptions) (configPreparedPlan, error) {
	scope, err := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if err != nil {
		return configPreparedPlan{}, err
	}
	exe, err := resolvedExecutable()
	if err != nil {
		return configPreparedPlan{}, err
	}
	legacy := options.Legacy
	if _, e := install.ConfigPreparationPackageIdentity(exe); e != nil {
		// Preserve only the existing explicitly opted-in uninstalled fake fixture.
		// This can never manufacture an installed generation/package authority.
		if os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") == "1" && e.Error() == "running_package_invalid" {
			legacy = true
		} else {
			return configPreparedPlan{}, e
		}
	}
	prepared, err := prepareConfigTaskPlan(ctx, state, exe, scope, plan, options.RequestSHA256, legacy, func(ctx context.Context, provider string) (store.ProviderState, error) {
		return providerSettingsCall(ctx, state, owner.OwnerCapability, provider, "status", nil)
	}, func(p taskPlan) error {
		if e := prepareTaskWorkspaces(handle, p.Tasks); e != nil {
			return e
		}
		if e := preflightTasks(p.Tasks); e != nil {
			return e
		}
		return preflightProviders(ctx, p.Tasks)
	}, func(ctx context.Context, original *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
		response, e := call(ctx, state, ipc.KindConfigGeneration, coordinator.ConfigGenerationRequest{OwnerCapability: owner.OwnerCapability, Action: "preparation-authority", OriginalAuthority: original})
		if e != nil {
			return contract.ConfigPreparationAuthority{}, e
		}
		var a contract.ConfigPreparationAuthority
		if e = decode(response.Payload, &a); e != nil {
			return a, e
		}
		return a, a.Validate()
	})
	if err == nil {
		err = preflightProviders(ctx, prepared.ResolvedPlan.Tasks)
	}
	return prepared, err
}

// A rejection fences this handle, not the shared use. Another explicit handle
// or low-level Submit may already have committed the same original plan/use.
// Only a future durable receiver-side rejection fence could authorize release.
func recordRejectedConfigSubmission(path string, h *taskHandle, prepared configPreparedPlan) error {
	h.Status, h.ControlFile = "rejected", ""
	if err := replacePrivateJSON(path, *h); err != nil {
		return err
	}
	if prepared.Use != nil && prepared.Use.Ref != nil {
		return codeError("config_rejection_use_retained")
	}
	return nil
}

func configGenerationEntry(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	action := args[0]
	if action != "status" && action != "publish" && action != "deactivate" && action != "retire" && action != "abandon" {
		return codeError("invalid_args")
	}
	fs := flag.NewFlagSet("provider snapshot "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private runtime state")
	requestPath := fs.String("request", "", "private exact generation operation")
	runID := fs.String("run-id", "", "permanently abandon this never-admitted preparation")
	expected := fs.String("expected-use-sha256", "", "optional original-use assertion")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || ((action == "status" || action == "abandon") != (*requestPath == "")) || (action == "abandon") != (*runID != "") || action != "abandon" && *expected != "" {
		return codeError("invalid_args")
	}
	var operation *install.ConfigGenerationOperation
	if action != "status" && action != "abandon" {
		raw, err := readBridgeRequestFile(*requestPath)
		if err != nil {
			return err
		}
		decoded, err := coordinator.DecodeConfigGenerationOperation(raw)
		if err != nil {
			return err
		}
		if decoded.Action != action {
			return codeError("config_generation_invalid")
		}
		operation = &decoded
	}
	if _, err := currentOwner(ctx); err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	ready, err := ensureServer(ctx, state)
	if err != nil {
		return err
	}
	capabilities, _ := ready["capabilities"].([]string)
	if !slices.Contains(capabilities, "owned_config_generation_v1") {
		return codeError("coordinator_upgrade_required")
	}
	var bound bytes.Buffer
	if err = ownerBind(ctx, []string{"--current", "--state-dir", state}, &bound); err != nil {
		return err
	}
	var owner controllerOwnerGrant
	if err = decode(bound.Bytes(), &owner); err != nil {
		return err
	}
	wireAction := action
	if action == "abandon" {
		wireAction = "abandon-preparation"
	}
	response, err := call(ctx, state, ipc.KindConfigGeneration, coordinator.ConfigGenerationRequest{OwnerCapability: owner.OwnerCapability, Action: wireAction, Operation: operation, RunID: *runID, ExpectedUseSHA256: *expected})
	if err != nil {
		return err
	}
	var result json.RawMessage
	if json.Unmarshal(response.Payload, &result) != nil {
		return codeError("invalid_response")
	}
	_, err = out.Write(append(result, '\n'))
	return err
}
