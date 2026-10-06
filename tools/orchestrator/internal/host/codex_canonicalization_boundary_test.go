//go:build darwin

package host

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Actual constructor, references and command output; synthetic sealed process
// provenance for the cold fixture. No sandbox/helper/provider is executed.
func TestCodexCanonicalizationCommandOnlyPermitsOriginalParentMetadata(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "cold_restore"}[cold], func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			if cold {
				h, _, g, inv, env = codexContinuationFixture(t)
				var err error
				h, err = NewIPC(h.spoolRoot, h.producerID)
				if err != nil || h.BindContinuationOwner("owner") != nil {
					t.Fatal("cold original Host owner")
				}
			}
			p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal("actual prepared state", err)
			}
			cmd, _, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
			if err != nil {
				t.Fatal("actual managed builder", err)
			}
			spool, attempt := h.spoolRoot, filepath.Join(h.spoolRoot, g.AttemptID)
			allowed := map[string]bool{
				"(allow file-read* (subpath " + strconv.Quote(p.Layout.State.Path) + "))": true,
				"(allow file-read-metadata (literal " + strconv.Quote(spool) + "))":       true,
				"(allow file-read-metadata (literal " + strconv.Quote(attempt) + "))":     true,
			}
			for _, line := range strings.Split(cmd.Args[1], "\n") {
				if strings.HasPrefix(line, "(allow file-read") {
					if !allowed[line] {
						t.Fatal("read/list/foreign ancestor authority expanded")
					}
					delete(allowed, line)
				}
			}
			if len(allowed) != 0 {
				t.Fatal("original parent metadata needed for owned home canonicalization missing")
			}
			if !strings.Contains(cmd.Args[1], "(deny file-read* (subpath "+strconv.Quote(spool)+"))") || strings.Count(cmd.Args[1], "(allow file-write* (subpath ") != 7 || strings.Count(cmd.Args[1], "(deny file-link)") != 1 {
				t.Fatal("original content/write/hardlink boundary drift")
			}
			for _, root := range []codexStateRoot{p.Layout.Sessions, p.Layout.Auxiliary.Runtime[0], p.Layout.Auxiliary.Runtime[1], p.Layout.Auxiliary.Runtime[2], p.Layout.SQLite, p.Layout.Auxiliary.Runtime[3], p.Layout.Auxiliary.Runtime[4]} {
				if !strings.Contains(cmd.Args[1], "(deny file-write-unlink file-write-mode file-write-owner (literal "+strconv.Quote(root.Path)+"))") {
					t.Fatal("immutable runtime root mutation denial removed")
				}
			}
		})
	}
}

func TestCodexCanonicalizationCommandRejectsReinterpretedSpool(t *testing.T) {
	for _, kind := range []string{"parent_as_spool", "attempt_as_spool", "foreign_attempt", "altered_attempt_scope"} {
		t.Run(kind, func(t *testing.T) {
			h, g, inv, env := codexOriginFixture(t)
			p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal(err)
			}
			spool := h.spoolRoot
			switch kind {
			case "parent_as_spool":
				spool = filepath.Dir(spool)
			case "attempt_as_spool":
				spool = filepath.Join(spool, g.AttemptID)
			case "foreign_attempt":
				spool = filepath.Join(spool, "other-attempt")
			case "altered_attempt_scope":
				p.Origin.Scope.AttemptID = "other-attempt"
			}
			if _, _, err := codexManagedCommand(context.Background(), p, env, spool); err == nil {
				t.Fatal("unbound spool/attempt accepted as read exception authority")
			}
		})
	}
}
