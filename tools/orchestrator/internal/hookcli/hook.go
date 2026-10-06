package hookcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Normalizer maps provider input to the frozen, non-secret v1 observation.
// It must be a bounded transform or cooperate with the shared Hook context;
// synchronous arbitrary Go callbacks cannot be forcibly preempted. Providers
// register at process startup, and raw input is never stored in spool.
type Normalizer func(context.Context, []byte, store.BridgeHookCapability) (contract.BridgeHookEventV1, error)

var providers = struct {
	sync.RWMutex
	byID map[string]Normalizer
}{byID: make(map[string]Normalizer)}

func RegisterProvider(id string, normalize Normalizer) error {
	if id == "" || normalize == nil {
		return errors.New("bridge_invalid_provider")
	}
	providers.Lock()
	defer providers.Unlock()
	if _, exists := providers.byID[id]; exists {
		return errors.New("bridge_provider_duplicate")
	}
	providers.byID[id] = normalize
	return nil
}

var stableCode = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func stableHookError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "bridge_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "bridge_cancelled"
	}
	if stableCode.MatchString(err.Error()) {
		return err.Error()
	}
	return "bridge_hook_failed"
}

// Run is deliberately silent on stdout for observation failures. Only the
// fully registered B01 wire may be written after BeginEmit has committed.
func Run(ctx context.Context, providerID, bindingPath string, in io.Reader, out, diagnostics io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	phase := &emissionPhase{}
	if providerID == "agy" {
		if !validAGYInvocation(ctx) {
			writeDiagnostic(ctx, diagnostics, "bridge_event_unsupported")
			return nil
		}
		boundedOut, closeOut, err := boundedOutput(ctx, out)
		if err != nil {
			writeDiagnostic(ctx, diagnostics, stableHookError(err))
			return nil
		}
		defer closeOut()
		out = boundedOut
	}
	err := run(ctx, providerID, bindingPath, in, out, phase)
	if providerID == "agy" && !phase.beginRequested && !phase.deliveryWriteStarted && ctx.Err() == nil {
		wire, renderErr := bridgecodec.NoMessageBytes(providerID, string(AGYPreInvocation))
		if renderErr == nil {
			n, writeErr := out.Write(wire)
			if writeErr != nil || n != len(wire) {
				err = errors.New("bridge_output_incomplete")
			}
		}
	}
	if err != nil {
		writeDiagnostic(ctx, diagnostics, stableHookError(err))
	}
	return nil
}

func writeDiagnostic(ctx context.Context, out io.Writer, code string) {
	if out == nil || code == "" {
		return
	}
	writeBestEffortDiagnostic(out, code+"\n")
}

func deadline(ctx context.Context) time.Time {
	d, ok := ctx.Deadline()
	if !ok {
		return time.Now()
	}
	return d
}

func newEventID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("bridge_random_unavailable")
	}
	return "hook-event-" + hex.EncodeToString(random[:]), nil
}

// beginRequested includes an uncertain Begin RPC: no fallback can follow it.
type emissionPhase struct{ beginRequested, deliveryWriteStarted bool }

func run(ctx context.Context, providerID, bindingPath string, in io.Reader, out io.Writer, phase *emissionPhase) error {
	providers.RLock()
	normalize := providers.byID[providerID]
	providers.RUnlock()
	if normalize == nil {
		return errors.New("bridge_provider_unknown")
	}
	if !filepath.IsAbs(bindingPath) || filepath.Base(filepath.Dir(bindingPath)) != "bridge" {
		return errors.New("bridge_capability_invalid")
	}
	capability, err := store.ReadBridgeCapability(bindingPath)
	if err != nil {
		return errors.New("bridge_capability_invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	boundedIn, closeIn, err := boundedInput(ctx, in)
	if err != nil {
		return err
	}
	defer closeIn()
	boundedOut, closeOut, err := boundedOutput(ctx, out)
	if err != nil {
		return err
	}
	defer closeOut()
	raw, err := io.ReadAll(io.LimitReader(boundedIn, 65537))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return err
		}
		return errors.New("bridge_input_unavailable")
	}
	if len(raw) > 65536 {
		return errors.New("bridge_input_too_large")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	event, err := normalize(ctx, raw, capability)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	event.EventID, err = newEventID()
	if err != nil {
		return err
	}
	if event.SchemaVersion != 1 || event.ProviderID != providerID || event.BindingID != capability.BindingID || event.OwnerEpoch != capability.OwnerEpoch {
		return errors.New("bridge_invalid_input")
	}
	eventRaw, err := json.Marshal(event)
	if err != nil {
		return errors.New("bridge_invalid_input")
	}
	if bytes.Contains(eventRaw, []byte(capability.HookCapability)) {
		return errors.New("bridge_public_secret")
	}
	if _, err := contract.DecodeBridgeHookEventV1(eventRaw); err != nil {
		return err
	}
	state := filepath.Dir(filepath.Dir(bindingPath))
	spool, err := openSpool(ctx, state, capability.BindingID)
	if err != nil {
		return err
	}
	if _, err := spool.put(ctx, event); err != nil {
		return err
	}
	records, err := spool.pending(ctx)
	if err != nil {
		return err
	}
	socket := filepath.Join(state, "coordinator.sock")
	for _, record := range records {
		if record.Event.BindingID != capability.BindingID || record.Event.OwnerEpoch != capability.OwnerEpoch {
			return errors.New("bridge_spool_stale")
		}
		request := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: capability.BindingID, OwnerEpoch: capability.OwnerEpoch, RequestID: record.Event.EventID}, HookCapability: capability.HookCapability}, Event: record.Event}
		response, err := callBridge(ctx, socket, ipc.KindBridgeIngest, request)
		if err != nil {
			return err
		}
		var ack contract.BridgeIngressAck
		if err := contract.DecodeBridgeStrict(response, &ack); err != nil {
			return errors.New("bridge_invalid_response")
		}
		if err := spool.ack(ctx, record, ack); err != nil {
			return err
		}
	}
	nativeHook := event.Metadata.NativeHook
	if !contract.BridgeEmitSupported(providerID, nativeHook) {
		return nil
	}
	base := contract.BridgeRequestBase{SchemaVersion: 1, BindingID: capability.BindingID, OwnerEpoch: capability.OwnerEpoch, RequestID: event.EventID}
	claim := contract.BridgeClaimRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: base, HookCapability: capability.HookCapability}, NativeHook: nativeHook}
	response, err := callBridge(ctx, socket, ipc.KindBridgeClaim, claim)
	if err != nil {
		return err
	}
	var reply contract.BridgeClaimReply
	if err := contract.DecodeBridgeStrict(response, &reply); err != nil || reply.ClaimToken == "" {
		return errors.New("bridge_invalid_response")
	}
	batch := reply.PublicBatch()
	wire, err := bridgecodec.WireBytes(batch, batch.AttemptID, providerID, nativeHook)
	if err != nil {
		return err
	}
	if bytes.Contains(wire, []byte(capability.HookCapability)) || bytes.Contains(wire, []byte(reply.ClaimToken)) {
		return errors.New("bridge_public_secret")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sum := sha256.Sum256(wire)
	wireHash := hex.EncodeToString(sum[:])
	emit := contract.BridgeEmitRequest{BridgeRequestBase: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: reply.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wireHash}
	phase.beginRequested = true
	response, err = callBridge(ctx, socket, ipc.KindBridgeBeginEmit, emit)
	if err != nil {
		return err
	}
	if !bytes.Equal(response, []byte(`{"status":"committed"}`)) {
		return errors.New("bridge_invalid_response")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	phase.deliveryWriteStarted = true
	n, writeErr := boundedOut.Write(wire)
	if writeErr != nil || n != len(wire) {
		return errors.New("bridge_output_incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	offered := contract.BridgeOfferedRequest{BridgeRequestBase: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: reply.ClaimToken, WireSHA256: wireHash}
	_, err = callBridge(ctx, socket, ipc.KindBridgeOffered, offered)
	return err
}

func callBridge(ctx context.Context, socket string, kind ipc.Kind, request any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("bridge_invalid_input")
	}
	id, err := newEventID()
	if err != nil {
		return nil, err
	}
	response, err := ipc.Call(ctx, socket, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: id, Payload: payload})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("bridge_coordinator_unavailable")
	}
	if response.RequestID != id {
		return nil, errors.New("bridge_invalid_response")
	}
	if response.Kind == ipc.KindError {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(response.Payload, &failure) != nil || !stableCode.MatchString(failure.Error) {
			return nil, errors.New("bridge_remote_error")
		}
		return nil, errors.New(failure.Error)
	}
	if response.Kind != ipc.KindResponse {
		return nil, errors.New("bridge_invalid_response")
	}
	return response.Payload, nil
}
