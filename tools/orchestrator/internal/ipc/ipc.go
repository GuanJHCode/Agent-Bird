package ipc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
)

const (
	Version        = 1
	MaxMessageSize = 64 * 1024
)

type Kind string

const (
	KindOwnerHostPending       Kind = "owner_host_pending"
	KindOwnerPrepare           Kind = "owner_prepare"
	KindOwnerLaunch            Kind = "owner_launch"
	KindOwnerLaunchStatus      Kind = "owner_launch_status"
	KindOwnerRPC               Kind = "original_owner_rpc"
	KindOwnerChallenge         Kind = "original_owner_challenge"
	KindOwnerCommand           Kind = "original_owner_command"
	KindOwnerTransportExchange Kind = "original_owner_transport_exchange"
	KindOwnerTransportControl  Kind = "original_owner_transport_control"
	KindOriginalOwnerBound     Kind = "original_owner_bound"
	KindHostHello              Kind = "host_hello"
	KindHostRecoveryScope      Kind = "host_recovery_scope"
	KindHostReady              Kind = "host_ready"
	KindHostStatus             Kind = "host_status"
	KindHostReconciled         Kind = "host_reconciled"
	KindHostDisposition        Kind = "host_disposition"
	KindLaunch                 Kind = "launch"
	KindStop                   Kind = "stop"
	KindEvent                  Kind = "event"
	KindHostProgress           Kind = "host_progress"
	KindReportCapability       Kind = "report_capability"
	KindReportEvent            Kind = "report_event"
	KindDurableAck             Kind = "durable_ack"
	KindReady                  Kind = "ready"
	KindOwnerBind              Kind = "owner_bind"
	KindProviderControl        Kind = "provider_control"
	KindConfigGeneration       Kind = "config_generation"
	KindSubmit                 Kind = "submit"
	KindStatus                 Kind = "status"
	KindSummary                Kind = "summary"
	KindCollect                Kind = "collect"
	KindWaitEvents             Kind = "wait_events"
	KindAck                    Kind = "ack"
	KindDiagnosticsRead        Kind = "diagnostics_read"
	KindDiagnosticsAck         Kind = "diagnostics_ack"
	KindDiagnosticsDelete      Kind = "diagnostics_delete"
	KindResume                 Kind = "resume"
	KindResumeContinuation     Kind = "resume_continuation"
	KindRetry                  Kind = "retry"
	KindBudgetRevise           Kind = "budget_revise"
	KindRework                 Kind = "rework"
	KindAccept                 Kind = "accept"
	KindAnswer                 Kind = "answer"
	KindRebindOwner            Kind = "rebind_owner"
	// KindRebindOwnerPublished is the only owner-rebind protocol that may
	// change a v2 launch.  The coordinator, rather than its caller, owns the
	// bootstrap publication lease and the durable receipt.
	KindRebindOwnerPublished Kind = "rebind_owner_published"
	KindRecoverHost          Kind = "recover_host"
	KindStopTask             Kind = "stop_task"
	KindResponse             Kind = "response"
	KindError                Kind = "error"
	KindACPBridgeBind        Kind = "acp_bridge_bind"
	KindACPBridgeActivate    Kind = "acp_bridge_activate"
	KindACPBridgeClaim       Kind = "acp_bridge_claim"
	KindACPBridgeBeginEmit   Kind = "acp_bridge_begin_emit"
	KindACPBridgeReceived    Kind = "acp_bridge_received"
	KindBridgeBind           Kind = "bridge_bind"
	KindBridgeActivate       Kind = "bridge_activate"
	KindBridgeIngest         Kind = "bridge_ingest"
	KindBridgeClaim          Kind = "bridge_claim"
	KindBridgeBeginEmit      Kind = "bridge_begin_emit"
	KindBridgeOffered        Kind = "bridge_offered"
	KindBridgeReceived       Kind = "bridge_received"
	KindBridgeQueryBatch     Kind = "bridge_query_batch"
	KindBridgeRedeliver      Kind = "bridge_redeliver"
	KindBridgeStatus         Kind = "bridge_status"
	KindBridgeUnbind         Kind = "bridge_unbind"
)

const (
	KindHostArtifactResolve          Kind = "host_artifact_retention_resolve"
	KindHostArtifactRetentionPlan    Kind = "host_artifact_retention_plan"
	KindHostArtifactRetentionApply   Kind = "host_artifact_retention_apply"
	KindHostGCEnable                 Kind = "host_gc_enable"
	KindHostGCDisable                Kind = "host_gc_disable"
	KindHostGCStatus                 Kind = "host_gc_status"
	KindReportArtifactResolve        Kind = "report_artifact_retention_resolve"
	KindReportArtifactRetentionPlan  Kind = "report_artifact_retention_plan"
	KindReportArtifactRetentionApply Kind = "report_artifact_retention_apply"
	KindReportGCEnable               Kind = "report_gc_enable"
	KindReportGCDisable              Kind = "report_gc_disable"
	KindReportGCStatus               Kind = "report_gc_status"
	KindDiagnosticsGCEnable          Kind = "diagnostics_gc_enable"
	KindDiagnosticsGCDisable         Kind = "diagnostics_gc_disable"
	KindDiagnosticsGCStatus          Kind = "diagnostics_gc_status"
	KindDiagnosticsRetentionPlan     Kind = "diagnostics_retention_plan"
	KindDiagnosticsRetentionApply    Kind = "diagnostics_retention_apply"
)

var (
	ErrMessageTooLarge = errors.New("ipc_message_too_large")
	ErrInvalidMessage  = errors.New("invalid_ipc_message")
)

type Envelope struct {
	Version   int             `json:"version"`
	Kind      Kind            `json:"kind"`
	RequestID string          `json:"request_id"`
	Epoch     uint64          `json:"epoch"`
	Payload   json.RawMessage `json:"payload"`
}

func Call(ctx context.Context, socketPath string, request Envelope) (Envelope, error) {
	if !filepath.IsAbs(socketPath) {
		return Envelope{}, ErrInvalidMessage
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Envelope{}, err
	}
	defer conn.Close()
	// DialContext only governs connection establishment. Closing the owned
	// connection also interrupts a blocked write or an incomplete response frame.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err = Write(conn, request); err != nil {
		if ctx.Err() != nil {
			return Envelope{}, ctx.Err()
		}
		return Envelope{}, err
	}
	response, err := Read(conn)
	if ctx.Err() != nil {
		return Envelope{}, ctx.Err()
	}
	return response, err
}

func Write(w io.Writer, message Envelope) error {
	if message.Version != Version || message.Kind == "" || message.RequestID == "" {
		return ErrInvalidMessage
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageSize {
		return ErrMessageTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func Read(r io.Reader) (Envelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Envelope{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return Envelope{}, ErrInvalidMessage
	}
	if size > MaxMessageSize {
		return Envelope{}, ErrMessageTooLarge
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return Envelope{}, err
	}
	var message Envelope
	if err := json.Unmarshal(data, &message); err != nil {
		return Envelope{}, ErrInvalidMessage
	}
	if message.Version != Version || message.Kind == "" || message.RequestID == "" {
		return Envelope{}, ErrInvalidMessage
	}
	return message, nil
}
