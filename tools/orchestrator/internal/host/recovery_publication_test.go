package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOriginalOwnerRecoveryPublicationRejectsStageDrift(t *testing.T) {
	for _, boundary := range []string{"before_verify", "before_rename"} {
		for _, drift := range []string{"replacement", "hardlink", "content"} {
			t.Run(boundary+"/"+drift, func(t *testing.T) {
				dir := privateRecoveryTestDirectory(t)
				s, err := openOwnerRecovery(dir, strings.Repeat("a", 64))
				if err != nil {
					t.Fatal(err)
				}
				defer s.close()
				prior := append([]byte(nil), s.file.bytes...)
				body := []byte("replacement ledger\n")
				mutate := func(name string) error {
					path := filepath.Join(dir, name)
					switch drift {
					case "replacement":
						if err := os.Rename(path, path+".original"); err != nil {
							return err
						}
						// Matching bytes cannot substitute another inode.
						return os.WriteFile(path, body, 0600)
					case "hardlink":
						return os.Link(path, path+".linked")
					default:
						return os.WriteFile(path, []byte("foreign ledger\n"), 0600)
					}
				}
				hooks := recoveryPublicationHooks{}
				if boundary == "before_verify" {
					hooks.beforeVerify = mutate
				} else {
					hooks.beforeRename = mutate
				}
				if err = s.file.persistBytesWithHooks(body, hooks); err == nil {
					t.Error("publication accepted substituted, linked, or modified stage")
				}
				if !bytes.Equal(s.file.bytes, prior) {
					t.Error("failed publication changed cached authority")
				}
				actual, readErr := os.ReadFile(s.file.path)
				if boundary == "before_verify" {
					if readErr != nil || !bytes.Equal(actual, prior) {
						t.Error("pre-publication rejection changed prior ledger", readErr)
					}
				} else {
					if !os.IsNotExist(readErr) {
						t.Error("untrusted publication was left at final name", readErr)
					}
					if err = s.verifyCurrent(); err == nil || !s.failed {
						t.Error("removed untrusted ledger did not poison owner authority")
					}
				}
			})
		}
	}
}

func TestOriginalOwnerRecoveryReadRejectsDetachedLedger(t *testing.T) {
	for _, operation := range []string{"ticket", "mutation"} {
		t.Run(operation, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			s, err := openOwnerRecovery(dir, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if _, err = s.reserveTransport(time.UnixMilli(100_000), "pending", 1, contract.OriginalOwnerTransport{PID: 10, Birth: "old"}); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = s.recordTransportStarted(contract.OriginalOwnerTransport{PID: 20, Birth: "child"}); err != nil {
				t.Fatal(err)
			}
			prior := append([]byte(nil), s.file.bytes...)
			foreign := append(append([]byte(nil), prior...), '\n')
			calls := 0
			s.file.afterReadOpen = func() error {
				calls++
				if err := os.Rename(s.file.path, s.file.path+".original"); err != nil {
					return err
				}
				return os.WriteFile(s.file.path, foreign, 0600)
			}
			if operation == "ticket" {
				_, err = s.ticketPending()
			} else {
				_, err = s.reserveCoordinator(time.UnixMilli(200_000))
			}
			if err == nil || !s.failed || calls != 1 {
				t.Error("detached read did not reject and poison authority", err, s.failed, calls)
			}
			actual, err := os.ReadFile(s.file.path)
			if err != nil || !bytes.Equal(actual, foreign) || !bytes.Equal(s.file.bytes, prior) {
				t.Error("detached read changed ledger or cached authority", err)
			}
		})
	}
}
