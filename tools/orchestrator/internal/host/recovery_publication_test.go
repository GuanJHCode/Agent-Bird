package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
