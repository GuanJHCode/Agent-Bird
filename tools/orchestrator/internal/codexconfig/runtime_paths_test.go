package codexconfig

import (
	"bytes"
	"reflect"
	"testing"
)

func TestBindRuntimePathsCopiesProjectionAndRequiresExactEffectiveValues(t *testing.T) {
	original, err := Project([]byte("model=\"gpt-fixed\"\nmodel_reasoning_effort=\"future-effort\"\n"), ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	before := original.ConfigTOML()
	controls := original.ManagedOverrides()
	bound, err := original.BindRuntimePaths("/owned/state/sqlite", "/owned/state/log")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, bound.ConfigTOML()) || !bytes.Equal(before, original.ConfigTOML()) || !reflect.DeepEqual(controls, bound.ManagedOverrides()) || len(controls) != 36 {
		t.Fatal("binding changed model/profile/config/controls")
	}
	if _, _, ok := original.RuntimePaths(); ok {
		t.Fatal("binding mutated original projection")
	}
	sqlite, log, ok := bound.RuntimePaths()
	if !ok || sqlite != "/owned/state/sqlite" || log != "/owned/state/log" {
		t.Fatal("exact runtime paths not bound")
	}
	cfg := runtimeEffective(original)
	cfg["sqlite_home"], cfg["log_dir"] = sqlite, log
	if err := bound.VerifyEffective(encodeEffective(t, cfg)); err != nil {
		t.Fatal("matching real policy rejected", err)
	}
	for _, key := range []string{"sqlite_home", "log_dir"} {
		for _, kind := range []string{"missing", "null", "type", "other"} {
			t.Run(key+"/"+kind, func(t *testing.T) {
				c := runtimeEffective(original)
				c["sqlite_home"], c["log_dir"] = sqlite, log
				switch kind {
				case "missing":
					delete(c, key)
				case "null":
					c[key] = nil
				case "type":
					c[key] = false
				case "other":
					c[key] = "/other/canonical/path"
				}
				if err := bound.VerifyEffective(encodeEffective(t, c)); err != ErrEffectivePolicy {
					t.Fatal("wrong runtime path accepted", err)
				}
			})
		}
	}
	// Copy ownership is independent even inside the owning package.
	bound.config[0] ^= 1
	bound.settings["model"] = "changed"
	if !bytes.Equal(before, original.ConfigTOML()) || original.settings["model"] != "gpt-fixed" {
		t.Fatal("bound projection aliases original bytes/settings")
	}
}

func TestBindRuntimePathsRejectsInvalidOrUnsupportedContext(t *testing.T) {
	p, err := Project(nil, ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"", "/owned/log"}, {"/owned/sqlite", ""}, {"relative", "/owned/log"}, {"/", "/owned/log"}, {"/owned/../sqlite", "/owned/log"}, {"/owned/sqlite", "/owned/sqlite"}, {string([]byte{'/', 0xff}), "/owned/log"}, {"/owned/\x00sqlite", "/owned/log"}, {"/" + string(make([]byte, 4096)), "/owned/log"}} {
		if _, err := p.BindRuntimePaths(pair[0], pair[1]); err == nil {
			t.Fatal("invalid runtime paths accepted")
		}
	}
	var nilProjection *Projection
	if _, err := nilProjection.BindRuntimePaths("/owned/sqlite", "/owned/log"); err == nil {
		t.Fatal("nil projection bound")
	}
	write, err := Project(nil, WorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.BindRuntimePaths("/owned/sqlite", "/owned/log"); err == nil {
		t.Fatal("unsupported managed write context bound")
	}
}

func runtimeEffective(p *Projection) map[string]any {
	c := syntheticEffective(ReadOnly, false)
	for key, value := range p.settings {
		c[key] = value
	}
	return c
}
