package host

import (
	"context"
	"strings"
	"testing"
)

// The trusted builder reads the actual installed system wrapper bytes; no
// executable or sandbox actor runs. Original process identity is synthetic.
func TestCodexThreadBindingRequiresActualLaunchWrapperIdentity(t *testing.T) {
	for _, kind := range []string{"exact_wrapper", "provider_as_wrapper", "wrapper_path", "wrapper_hash", "provider_pin"} {
		t.Run(kind, func(t *testing.T) {
			h, p, g, id, b := codexThreadFixture(t)
			_, launched, err := codexManagedCommand(context.Background(), p, []string{"HOME=/synthetic-original", "CODEX_HOME=" + p.Source.Home}, h.spoolRoot)
			if err != nil {
				t.Fatal(err)
			}
			id.Executable, id.ExecutableSHA256 = launched.Launch.Path, launched.Launch.SHA256
			switch kind {
			case "provider_as_wrapper":
				id.Executable, id.ExecutableSHA256 = p.Origin.Lock.Binary.Path, p.Origin.Lock.Binary.SHA256
			case "wrapper_path":
				id.Executable = "/different/wrapper"
			case "wrapper_hash":
				id.ExecutableSHA256 = strings.Repeat("0", 64)
			case "provider_pin":
				launched.Origin.Lock.Binary.SHA256 = strings.Repeat("0", 64)
			}
			bound, err := h.bindManagedCodexThread(context.Background(), launched, g, id, b)
			if kind == "exact_wrapper" {
				if err != nil || bound.Session.EventID == "" {
					t.Fatal("original real exec wrapper was rejected", err)
				}
			} else if err == nil {
				t.Fatal("wrapper/provider pin identities conflated")
			}
		})
	}
}
