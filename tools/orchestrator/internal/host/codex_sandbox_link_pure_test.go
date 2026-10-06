//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Pure bounded FS/codec/net.Pipe only; no helper process, launcher, sandbox,
// kernel-denial assertion or TestMain. Link/Symlink syscall negatives are
// prepared for ROOT's separately authorized OS actor, never run here.
func TestCodexSandboxLinkPlanHasValidInitialInventoryAndChildOnlyAliases(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	ctx := context.Background()
	p, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, p, err := codexManagedCommand(ctx, p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan, paths, err := codexSandboxLinkBuildPlan(filepath.Dir(p.Source.Home), p, h.spoolRoot, inv.WorkingDirectory(), p.Source.Home)
	if err != nil || codexVerifyInitialPrepared(ctx, p) != nil || codexVerifyManagedReferences(ctx, p) != nil {
		t.Fatal("plan preparation drifted real initial inventory", err)
	}
	if len(plan.Operations) != 219 {
		t.Fatal("closed operation inventory changed", len(plan.Operations))
	}
	names, kinds := map[string]bool{}, map[string]int{}
	for _, op := range plan.Operations {
		names[op.Name], kinds[op.Kind] = true, kinds[op.Kind]+1
		if op.Kind == "link" || op.Kind == "symlink" {
			if _, e := os.Lstat(op.Other); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("parent inserted alias before Start")
			}
		}
	}
	if kinds["link"] != 21 || kinds["absent"] != 21 || kinds["singlelink"] != 7 || kinds["symlink"] != 2 {
		t.Fatal("worker creation/absence/invariant controls missing", kinds)
	}
	for _, root := range []string{"sessions", "skills", "home_tmp", "locks", "sqlite", "log", "tmp"} {
		for _, name := range []string{"source_link_", "source_absent_", "protected_link_", "protected_absent_", "runtime_link_", "runtime_absent_", "runtime_unchanged_"} {
			if !names[name+root] {
				t.Fatal("required per-root link control absent", name, root)
			}
		}
		for _, suffix := range []string{"create", "read", "save"} {
			if !names[root+"_"+suffix] {
				t.Fatal("ordinary runtime positive missing")
			}
		}
	}
	for _, name := range []string{"source_symlink_create", "source_symlink_read", "source_symlink_write", "protected_symlink_create", "protected_symlink_read", "protected_symlink_write", "rollout_save", "installation_save"} {
		if !names[name] {
			t.Fatal("alias/atomic/literal control absent")
		}
	}
	for i := range codexSQLiteFamilies {
		for j := range 3 {
			name := "sqlite_" + string(rune('0'+i)) + "_" + string(rune('0'+j)) + "_save"
			if !names[name] {
				t.Fatal("SQLite family/sidecar save missing")
			}
		}
	}
	a, err := codexSandboxSnapshot(paths)
	if err != nil {
		t.Fatal(err)
	}
	b, err := codexSandboxSnapshot(paths)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("parent changed preserved synthetic originals")
	}
	for _, path := range []string{filepath.Join(p.Source.Home, "alias-probe"), filepath.Join(h.spoolRoot, "alias-probe")} {
		if a[path].Links != 1 {
			t.Fatal("initial alias source not singlelink")
		}
	}
	body, _ := json.Marshal(plan)
	var decoded codexSandboxPlan
	if codexSandboxDecode(body, &decoded) != nil || !reflect.DeepEqual(plan, decoded) {
		t.Fatal("strict bounded plan did not round trip")
	}
	// Only actual helper hash is used for tail replacement; never execute it.
	helper := filepath.Join(inv.WorkingDirectory(), "synthetic-helper")
	if os.WriteFile(helper, []byte("NEVER_EXECUTE"), 0700) != nil {
		t.Fatal("helper fixture")
	}
	changed, err := codexSandboxLinkCommand(ctx, cmd, p, helper, codexDigest([]byte("NEVER_EXECUTE")))
	if err != nil || changed.Path != cmd.Path || changed.PinnedPath != cmd.PinnedPath || changed.PinnedSHA256 != cmd.PinnedSHA256 || changed.Dir != cmd.Dir || changed.ExactEnv != cmd.ExactEnv || !reflect.DeepEqual(changed.Env, cmd.Env) || changed.Args[1] != cmd.Args[1] || changed.Args[2] != helper || changed.Args[3] != "-test.run=^TestManagedCodexSandboxLinkAPIHelperProcess$" || changed.Args[5] != "codex-sandbox-link-api" {
		t.Fatal("helper changed actual builder scope/pins", err)
	}
}

func TestCodexSandboxLinkPlanStrictBoundsAndNoPolicyInference(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{{Name: "link", Kind: "link", Path: filepath.Join(base, "source"), Other: filepath.Join(base, "target"), Expect: "permission"}}}
	if codexSandboxLinkValidatePlan(plan) != nil || codexSandboxValidatePlan(plan) == nil {
		t.Fatal("new helper must not expand old actor's closed kinds")
	}
	for _, kind := range []string{"unknown", "other_outside", "empty_other", "unexpected_other", "duplicate", "too_many", "unknown_key"} {
		t.Run(kind, func(t *testing.T) {
			p := plan
			p.Operations = append([]codexSandboxOperation(nil), plan.Operations...)
			switch kind {
			case "unknown":
				p.Operations[0].Kind = "open_any"
			case "other_outside":
				p.Operations[0].Other = "/private/tmp/foreign"
			case "empty_other":
				p.Operations[0].Other = ""
			case "unexpected_other":
				p.Operations[0].Kind = "absent"
			case "duplicate":
				p.Operations = append(p.Operations, p.Operations[0])
			case "too_many":
				p.Operations = make([]codexSandboxOperation, 257)
			case "unknown_key":
				body, _ := json.Marshal(p)
				body = []byte(strings.Replace(string(body), `"version":1`, `"version":1,"extra":null`, 1))
				var out codexSandboxPlan
				if codexSandboxDecode(body, &out) == nil {
					t.Fatal("unknown field swallowed")
				}
				return
			}
			if codexSandboxLinkValidatePlan(p) == nil {
				t.Fatal("unsafe plan admitted")
			}
		})
	}
	if codexSandboxMatched("permission", os.ErrNotExist) || codexSandboxMatched("permission", nil) || codexSandboxLinkOperation(codexSandboxOperation{Kind: "absent", Path: base}) == nil {
		t.Fatal("missing target/directory misclassified as permission/absence")
	}
}

func TestCodexSandboxLinkProtocolUsesOriginalNetPipeAndJoins(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "runtime-file")
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{
		{Name: "create", Kind: "create", Path: path, Expect: "success"},
		{Name: "save", Kind: "replace", Path: path, Other: path + "-save", Expect: "success"},
		{Name: "unchanged", Kind: "singlelink", Path: path, Expect: "success"},
		{Name: "absent", Kind: "absent", Path: path + "-absent", Expect: "success"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	deadline, _ := ctx.Deadline()
	_ = a.SetDeadline(deadline)
	_ = b.SetDeadline(deadline)
	joined := make(chan error, 1)
	written := make(chan error, 1)
	go func() { joined <- codexSandboxLinkProtocol(ctx, b, b) }()
	body, _ := json.Marshal(plan)
	go func() { _, e := a.Write(append(body, '\n')); written <- e }()
	var wire json.RawMessage
	if json.NewDecoder(io.LimitReader(a, codexSandboxWireLimit+1)).Decode(&wire) != nil {
		t.Fatal("bounded reply")
	}
	var report codexSandboxReport
	if codexSandboxDecode(wire, &report) != nil || report.Incomplete || report.Planned != 4 || len(report.Observations) != 4 {
		t.Fatal("exact helper protocol")
	}
	for _, ch := range []<-chan error{written, joined} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("original pipe job not joined")
		}
	}
	for _, op := range report.Observations {
		if !op.Matched || op.ErrorClass != "none" {
			t.Fatal("ordinary FS control")
		}
	}
}
