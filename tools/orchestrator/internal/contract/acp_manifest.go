package contract

import (
	"crypto/sha256"
	"encoding/hex"
)

// GrokACPManifest describes the origin protocol adapter, not a Hook or a
// qualification of any particular Grok executable/account/tool profile.
const GrokACPManifest = `{"schema_version":1,"provider_id":"grok","transport_mode":"grok_acp_push_v1","protocol":"acp-v1","codec":"acp-prompt-v1","authority":"exact-original-controller-and-child","permission_requests":"deny","completion":"owner-observed-protocol-receipt","prompt_max_bytes":8192,"frame_max_bytes":65536}`

func GrokACPManifestSHA256() string {
	sum := sha256.Sum256([]byte("agent-bird-acp-manifest-v1\x00" + GrokACPManifest))
	return hex.EncodeToString(sum[:])
}
