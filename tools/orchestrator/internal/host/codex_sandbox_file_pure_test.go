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
	"syscall"
	"testing"
	"time"
)

// These selected tests perform bounded synthetic FS and codec/builder reads
// only. Neither TestMain nor any helper/Start/sandbox/provider is invoked.
func TestCodexSandboxFilePlanCoversExplicitRootsFamiliesAndProtectedAliases(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	source := strings.TrimPrefix(env[1], "CODEX_HOME=")
	base := filepath.Dir(source)
	plan, paths, err := codexSandboxBuildPlan(base, p, h.spoolRoot, inv.WorkingDirectory(), source)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	var decoded codexSandboxPlan
	if err != nil || codexSandboxDecode(body, &decoded) != nil || !reflect.DeepEqual(plan, decoded) || len(body) > codexSandboxWireLimit {
		t.Fatal("bounded exact plan codec")
	}
	names := map[string]bool{}
	for _, op := range plan.Operations {
		names[op.Name] = true
	}
	for _, name := range []string{"sessions", "skills", "home_tmp", "locks", "sqlite", "log", "tmp"} {
		for _, kind := range []string{"create", "read", "save"} {
			if !names[name+"_"+kind] {
				t.Fatal("missing allowed root operation")
			}
		}
		for _, kind := range []string{"remove", "rename", "chmod"} {
			if !names["root_"+name+"_"+kind] {
				t.Fatal("missing root mutation")
			}
		}
	}
	for _, name := range []string{"workspace", "source_config", "source_auth", "owned_config", "owned_auth", "origin", "thread", "seal", "control"} {
		for _, kind := range []string{"read", "overwrite", "remove", "replace"} {
			if !names[name+"_"+kind] {
				t.Fatal("missing synthetic protected operation")
			}
		}
	}
	if len(plan.Operations) != 168 {
		t.Fatalf("closed operation inventory drift: %d", len(plan.Operations))
	}
	if names["source_symlink_write"] != true || !names["source_hardlink_write"] || !names["protected_symlink_write"] || !names["protected_hardlink_write"] || !names["rollout_save"] || !names["installation_save"] {
		t.Fatal("required aliases/rollout/literal missing")
	}
	a, err := codexSandboxSnapshot(paths)
	if err != nil {
		t.Fatal(err)
	}
	b, err := codexSandboxSnapshot(paths)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("stable synthetic prelaunch snapshot")
	}
	// Dedicated aliases must not change constructor's original config/auth
	// nlink/refs. This is a preparation check, not kernel policy acceptance.
	if codexVerifyManagedReferences(context.Background(), p) != nil {
		t.Fatal("aliases changed original managed refs")
	}
	for _, name := range []string{"source", "protected"} {
		alias := filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, name+"-symlink")
		if _, err := os.Readlink(alias); err != nil {
			t.Fatal("real synthetic symlink")
		}
		st, err := os.Stat(filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, name+"-hardlink"))
		if err != nil || st.Sys().(*syscall.Stat_t).Nlink != 2 {
			t.Fatal("real synthetic hardlink")
		}
	}
}
func TestCodexSandboxFilePlanRejectsAmbiguousAndUnboundedInputs(t *testing.T) {
	base := "/private/tmp/ab-c06-sandbox-pure"
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{{"probe", "create", base + "/path", "", "success"}}}
	for _, kind := range []string{"duplicate_name", "unknown_kind", "escape", "relative", "other", "expect", "count", "unknown_field", "duplicate_key", "null", "case_alias", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			bad := plan
			bad.Operations = append([]codexSandboxOperation(nil), plan.Operations...)
			switch kind {
			case "duplicate_name":
				bad.Operations = append(bad.Operations, bad.Operations[0])
			case "unknown_kind":
				bad.Operations[0].Kind = "shell"
			case "escape":
				bad.Operations[0].Path = "/private/tmp/foreign"
			case "relative":
				bad.Operations[0].Path = base + "/a/../b"
			case "other":
				bad.Operations[0].Other = base + "/extra"
			case "expect":
				bad.Operations[0].Expect = "any_error"
			case "count":
				bad.Operations = make([]codexSandboxOperation, codexSandboxOperationLimit+1)
			}
			body, _ := json.Marshal(bad)
			switch kind {
			case "unknown_field":
				body = append([]byte(`{"unknown":1,`), body[1:]...)
			case "duplicate_key":
				body = append([]byte(`{"version":1,`), body[1:]...)
			case "null":
				body = []byte(`{"version":1,"base":"/private/tmp/x","operations":null}`)
			case "case_alias":
				body = []byte(strings.Replace(string(body), `"version"`, `"Version"`, 1))
			case "oversize":
				body = make([]byte, codexSandboxWireLimit+1)
			}
			var out codexSandboxPlan
			if codexSandboxDecode(body, &out) == nil && codexSandboxValidatePlan(out) == nil {
				t.Fatal("unsafe or ambiguous input accepted")
			}
		})
	}
}
func TestCodexSandboxPermissionMatcherDoesNotConfuseAbsentOrNonemptyWithDenial(t *testing.T) {
	for _, err := range []error{syscall.ENOENT, syscall.ENOTEMPTY, syscall.EEXIST, context.Canceled, context.DeadlineExceeded, errors.New("unknown")} {
		if codexSandboxMatched("permission", &os.PathError{Op: "probe", Path: "synthetic", Err: err}) {
			t.Fatal("non-permission became kernel denial proof")
		}
	}
	for _, err := range []error{syscall.EACCES, syscall.EPERM} {
		if !codexSandboxMatched("permission", &os.PathError{Op: "probe", Path: "synthetic", Err: err}) {
			t.Fatal("actual permission not classified")
		}
	}
	if !codexSandboxMatched("success", nil) || codexSandboxMatched("permission", nil) || codexSandboxErrorClass(nil) != "none" {
		t.Fatal("success/permission classes mixed")
	}
}
func TestCodexSandboxFileAPIPureEffectsAndFirstMismatchHalt(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "probe")
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{{"create", "create", path, "", "success"}, {"read", "read", path, "", "success"}, {"atomic_save", "replace", path, path + "-temp", "success"}}}
	r := codexSandboxRun(context.Background(), plan)
	if r.Incomplete || len(r.Observations) != 3 {
		t.Fatal("bounded pure file API failed")
	}
	for _, v := range r.Observations {
		if !v.Matched || v.ErrorClass != "none" {
			t.Fatal("pure operation failed")
		}
	}
	body, _ := os.ReadFile(path)
	if string(body) != "SYNTHETIC_ATOMIC_SAVE\n" {
		t.Fatal("actual atomic save not exercised")
	}
	plan.Operations = []codexSandboxOperation{{"actual_denial_missing", "read", path + "-absent", "", "permission"}, {"never_run", "create", path + "-never", "", "success"}}
	r = codexSandboxRun(context.Background(), plan)
	if !r.Incomplete || len(r.Observations) != 1 || r.Observations[0].ErrorClass != "enoent" || r.Observations[0].Matched {
		t.Fatal("first missing error masquerades as permission or later operation ran")
	}
	if _, err := os.Lstat(path + "-never"); !os.IsNotExist(err) {
		t.Fatal("later operation started after mismatch")
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), base) || strings.Contains(string(encoded), "SYNTHETIC") {
		t.Fatal("report disclosed paths/file values")
	}
}
func TestCodexSandboxCommandPreservesBuilderAndIndependentPins(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, p, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(filepath.Dir(inv.WorkingDirectory()), "helper")
	body := []byte("synthetic executable, never run")
	if os.WriteFile(helper, body, 0700) != nil {
		t.Fatal("helper fixture")
	}
	next, err := codexSandboxCommand(context.Background(), cmd, p, helper, codexDigest(body))
	if err != nil {
		t.Fatal(err)
	}
	if next.Path != cmd.Path || next.PinnedPath != cmd.PinnedPath || next.PinnedSHA256 != cmd.PinnedSHA256 || next.Dir != cmd.Dir || next.ExactEnv != cmd.ExactEnv || !reflect.DeepEqual(next.Env, cmd.Env) || !reflect.DeepEqual(next.Args[:2], cmd.Args[:2]) || cmd.Args[2] != cmd.PinnedPath || next.Args[2] != helper {
		t.Fatal("production scope/env/pins changed")
	}
	if _, err := codexSandboxCommand(context.Background(), cmd, p, helper, p.Origin.Lock.Binary.SHA256); err == nil {
		t.Fatal("provider pin treated as actual helper pin")
	}
	bad := cmd
	bad.PinnedSHA256 = codexDigest([]byte("foreign"))
	if _, err := codexSandboxCommand(context.Background(), bad, p, helper, codexDigest(body)); err == nil {
		t.Fatal("provider drift accepted")
	}
}

func TestCodexSandboxFileAPIProtocolUsesRetainedPipeWithoutStdinEOF(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{{"create", "create", filepath.Join(base, "file"), "", "success"}}}
	input, peer := net.Pipe()
	defer input.Close()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input.SetDeadline(time.Now().Add(time.Second))
	peer.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() { done <- codexSandboxProtocol(ctx, input, input) }()
	body, _ := json.Marshal(plan)
	if n, err := peer.Write(body); err != nil || n != len(body) {
		t.Fatal("original full write")
	}
	var raw json.RawMessage
	if json.NewDecoder(io.LimitReader(peer, codexSandboxWireLimit+1)).Decode(&raw) != nil {
		t.Fatal("helper awaited nonexistent stdin EOF")
	}
	var report codexSandboxReport
	if codexSandboxDecode(raw, &report) != nil || report.Incomplete || len(report.Observations) != 1 || !report.Observations[0].Matched {
		t.Fatal("exact bounded reply")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("original helper job not joined")
	}
}
