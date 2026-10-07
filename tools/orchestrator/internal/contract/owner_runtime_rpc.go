package contract

// These messages belong to the private transport/owner enrollment protocol.
// Kernel observations and launch/control credentials are deliberately absent.
type OwnerHostPending struct {
	Version              int    `json:"version"`
	HostID               string `json:"host_id"`
	CoordinatorEpoch     uint64 `json:"coordinator_epoch"`
	CoordinatorPublicKey string `json:"coordinator_public_key"`
	OwnerID              string `json:"owner_id,omitempty"`
}
type OwnerPrepareRequest struct {
	Version               int    `json:"version"`
	BootstrapID           string `json:"bootstrap_id"`
	BootstrapTokenSHA256  string `json:"bootstrap_token_sha256"`
	StartupDeadlineUnixMS int64  `json:"startup_deadline_unix_ms"`
}

func (p OwnerPrepareRequest) Validate(requestID string) error {
	if p.Version != 1 || !ownerText(p.BootstrapID, 256) || !ownerText(requestID, 256) || !ownerDigest(p.BootstrapTokenSHA256) || p.StartupDeadlineUnixMS <= 0 {
		return ownerInvalid()
	}
	return nil
}

type OwnerPrepared struct {
	Intent               OriginalOwnerIntent `json:"intent"`
	CoordinatorPublicKey string              `json:"coordinator_public_key"`
	RPCSocketPath        string              `json:"rpc_socket_path"`
}
type OwnerRPCOpen struct {
	Version int                   `json:"version"`
	OwnerID string                `json:"owner_id"`
	Owner   OriginalOwnerIdentity `json:"owner"`
}
type OwnerBindReply struct {
	Binding OriginalOwnerBinding `json:"binding"`
	Permit  OwnerControlPermit   `json:"permit"`
}
type OwnerAttachRequest struct {
	ExpectedControlRevision uint64                 `json:"expected_control_revision"`
	OldHost                 OriginalOwnerTransport `json:"old_host"`
	NewHost                 OriginalOwnerTransport `json:"new_host"`
	Pending                 *OwnerTransportPending `json:"pending,omitempty"`
}
type OwnerRuntimeState struct {
	Binding                OriginalOwnerBinding `json:"binding"`
	Permit                 OwnerControlPermit   `json:"permit"`
	AppliedControlRevision uint64               `json:"applied_control_revision"`
}
