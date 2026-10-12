package contract

// OwnerRecoveryScopeRequest carries no caller-selected filesystem authority.
type OwnerRecoveryScopeRequest struct {
	Version int `json:"version"`
}
type OwnerRecoveryScopeReply struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
}

func OwnerRecoveryScopeRequestID(scope OriginalOwnerScope, owner OriginalOwnerIdentity) (string, error) {
	digest, err := OriginalOwnerRecoveryScopeSHA256(scope, owner)
	if err != nil {
		return "", err
	}
	return "recovery-scope:" + digest, nil
}
