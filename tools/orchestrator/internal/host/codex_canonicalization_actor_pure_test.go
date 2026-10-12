//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Pure FS/codec/net.Pipe preparation. Permission expectations are only plan
// contracts here; no sandbox-exec/helper/CLI/provider or TestMain is invoked.
func TestCodexCanonicalizationSandboxPlanRetains219AndStrictScope(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	ctx := context.Background()
	p, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	plan, paths, err := codexSandboxLinkBuildPlan(filepath.Dir(p.Source.Home), p, h.spoolRoot, inv.WorkingDirectory(), p.Source.Home)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]codexSandboxOperation(nil), plan.Operations...)
	plan, paths, err = codexSandboxCanonicalizationExtendPlan(plan, paths, p, h.spoolRoot)
	if err != nil || len(plan.Operations) != 233 || !reflect.DeepEqual(original, plan.Operations[:219]) || codexVerifyInitialPrepared(ctx, p) != nil || codexVerifyManagedReferences(ctx, p) != nil {
		t.Fatal("original inventory/219 operations drifted", err)
	}
	expected := map[string]string{
		"owned_home_canonicalize": "success", "original_spool_metadata": "success", "original_attempt_metadata": "success",
		"original_spool_list": "permission", "original_attempt_list": "permission", "original_spool_control_read": "permission",
		"other_attempt_metadata": "permission", "other_attempt_list": "permission", "other_attempt_file_metadata": "permission", "other_attempt_file_read": "permission",
		"other_segment_metadata": "permission", "other_segment_list": "permission", "other_segment_file_metadata": "permission", "other_segment_file_read": "permission",
	}
	for _, op := range plan.Operations[219:] {
		if expected[op.Name] != op.Expect {
			t.Fatal("closed canonicalization boundary expectation drift")
		}
		delete(expected, op.Name)
	}
	if len(expected) != 0 || len(paths) == 0 {
		t.Fatal("missing boundary/preserved original")
	}
	before, err := codexSandboxSnapshot(paths)
	if err != nil {
		t.Fatal(err)
	}
	after, err := codexSandboxSnapshot(paths)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("preparation changed synthetic originals")
	}
	for _, kind := range []string{"metadata", "list", "canonicalize"} {
		op := codexSandboxOperation{Name: "bounded", Kind: kind, Path: p.Layout.Home.Path, Expect: "success"}
		valid := codexSandboxPlan{Version: 1, Base: filepath.Dir(p.Source.Home), Operations: []codexSandboxOperation{op}}
		if codexSandboxLinkValidatePlan(valid) != nil || codexSandboxValidatePlan(valid) == nil {
			t.Fatal("new kinds must remain exclusive to reviewed link helper")
		}
		valid.Operations[0].Other = filepath.Join(p.Layout.Home.Path, "other")
		if codexSandboxLinkValidatePlan(valid) == nil {
			t.Fatal("single path metadata/list/canonicalization accepted extra path")
		}
	}
}

func TestCodexCanonicalizationSandboxProtocolUsesRealFileAPIsAndOriginalPipe(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.WriteFile(filepath.Join(base, "synthetic"), []byte("SYNTHETIC_CONTENT_NEVER_EXPORTED"), 0600) != nil {
		t.Fatal("owned FS fixture")
	}
	plan := codexSandboxPlan{Version: 1, Base: filepath.Dir(base), Operations: []codexSandboxOperation{
		{Name: "canonical", Kind: "canonicalize", Path: base, Expect: "success"},
		{Name: "metadata", Kind: "metadata", Path: base, Expect: "success"},
		{Name: "list", Kind: "list", Path: base, Expect: "success"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	d, _ := ctx.Deadline()
	_ = a.SetDeadline(d)
	_ = b.SetDeadline(d)
	joined, written := make(chan error, 1), make(chan error, 1)
	go func() { joined <- codexSandboxLinkProtocol(ctx, b, b) }()
	body, _ := json.Marshal(plan)
	go func() { _, e := a.Write(append(body, '\n')); written <- e }()
	var raw json.RawMessage
	if json.NewDecoder(io.LimitReader(a, codexSandboxWireLimit+1)).Decode(&raw) != nil {
		t.Fatal("bounded reply")
	}
	var report codexSandboxReport
	if codexSandboxDecode(raw, &report) != nil || report.Incomplete || len(report.Observations) != 3 {
		t.Fatal("real file API protocol failed")
	}
	for _, obs := range report.Observations {
		if !obs.Matched || obs.ErrorClass != "none" {
			t.Fatal("canonical/metadata/list positive file API failed")
		}
	}
	if strings.Contains(string(raw), base) || strings.Contains(string(raw), "SYNTHETIC_CONTENT") {
		t.Fatal("path/file scalar exported")
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
}
