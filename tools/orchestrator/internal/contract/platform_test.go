package contract

import (
	"bytes"
	"testing"
)

func TestCompiledPlatformContractGates(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows", "unknown"} {
		t.Run(goos, func(t *testing.T) {
			p := CompiledPlatformContract(goos, "arm64")
			seen := map[string]bool{}
			for _, c := range p.Capabilities {
				if seen[c.ID] || c.Version != 1 || c.Requires == nil {
					t.Fatalf("invalid capability: %+v", c)
				}
				seen[c.ID] = true
				if (c.ID == "filtered_broker_carrier" || c.ID == "filtered_conversion_freeze") && ((goos == "darwin") != (c.Gate == "internal")) {
					t.Fatalf("wrong platform gate %+v", c)
				}
				if c.ID == "original_owner_supervision" && ((goos == "darwin" || goos == "linux") != (c.Gate == "public")) {
					t.Fatalf("wrong supervision gate %+v", c)
				}
			}
			a, hash, err := p.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			b, same, _ := CompiledPlatformContract(goos, "arm64").CanonicalJSON()
			if !bytes.Equal(a, b) || hash != same {
				t.Fatal("unstable contract")
			}
			p.Version++
			_, changed, _ := p.CanonicalJSON()
			if hash == changed {
				t.Fatal("unbound version")
			}
		})
	}
}

func TestPlatformEventVocabularyIncludesLaunchRejected(t *testing.T) {
	p := CompiledPlatformContract("darwin", "arm64")
	seen := map[string]bool{}
	for _, kind := range p.Events.Kinds {
		if seen[kind] {
			t.Fatalf("duplicate event %s", kind)
		}
		seen[kind] = true
	}
	if !seen[EventLaunchRejected] {
		t.Fatal("launch_rejected missing")
	}
}
