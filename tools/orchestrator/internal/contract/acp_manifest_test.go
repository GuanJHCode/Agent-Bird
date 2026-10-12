package contract

import "testing"

func TestGrokACPManifestProtocolPin(t *testing.T) {
	// Independently computed public v1 pin. A semantic adapter change must
	// deliberately version its manifest instead of reusing an old binding.
	if got := GrokACPManifestSHA256(); got != "7bfbb2b5e16711058e7895884ff50311292571fe5f46ecd94d95ecd2883a4d51" {
		t.Fatalf("ACP adapter v1 manifest changed: %s", got)
	}
}
