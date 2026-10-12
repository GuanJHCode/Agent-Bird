package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newCodexSealableFixture(t *testing.T) (codexStateLayout, codexSlotsFixture, codexSourceReferences, codexOwnedReferences) {
	t.Helper()
	layout, f := newCodexPrimaryFixture(t)
	sourceHome := newCodexSourceFixture(t, "model=\"gpt-fixed\"\n")
	source, projection, err := codexCaptureSourceReferences(context.Background(), sourceHome, codexSourceControl{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Home.Path, "config.toml"), projection.ConfigTOML(), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(layout.Home.Path, "auth.json")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sourceHome, "auth.json"), link); err != nil {
		t.Fatal(err)
	}
	owned, err := codexCaptureOwnedReferences(context.Background(), layout.Home, source)
	if err != nil {
		t.Fatal(err)
	}
	return layout, f, source, owned
}

func TestCodexSealableCaptureRetainsSourceAndOwnedReferences(t *testing.T) {
	layout, f, source, owned := newCodexSealableFixture(t)
	remaining := codexStateTotalLimit
	observed, err := codexCaptureSealableObservation(context.Background(), layout, f.binding, f.installation, source, owned, codexStateReadControl{Remaining: &remaining})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Physical.State.Slots) != 20 || observed.Source.Home != source.Home || observed.Owned.Config.SHA256 != source.ProjectedSHA256 {
		t.Fatal("references lost before protected publisher")
	}
}

func TestCodexSealableCaptureRejectsSourceDriftDuringLastAuxRead(t *testing.T) {
	for _, kind := range []string{"source_config", "source_auth"} {
		t.Run(kind, func(t *testing.T) {
			layout, f, source, owned := newCodexSealableFixture(t)
			if err := os.WriteFile(filepath.Join(layout.Auxiliary.Runtime[0].Path, "last"), []byte("last auxiliary input"), 0644); err != nil {
				t.Fatal(err)
			}
			remaining := codexStateTotalLimit
			reads := 0
			reached := false
			_, err := codexCaptureSealableObservation(context.Background(), layout, f.binding, f.installation, source, owned, codexStateReadControl{Remaining: &remaining, BeforeVerify: func(fd int) {
				reads++
				if reads != 21 {
					return
				}
				if fd < 0 {
					t.Fatal("original final auxiliary FD missing")
				}
				reached = true
				name, body := "config.toml", []byte("model=\"gpt-fixed\"\n")
				if kind == "source_auth" {
					name = "auth.json"
					body = []byte("SYNTHETIC_AUTH_NEVER_READ")
				}
				tmp := filepath.Join(source.Home, "replacement")
				if err := os.WriteFile(tmp, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, filepath.Join(source.Home, name)); err != nil {
					t.Fatal(err)
				}
			}})
			if !reached || reads != 21 {
				t.Fatal("real last auxiliary original read not reached")
			}
			if !errors.Is(err, errCodexStateFile) {
				t.Fatal("source identity drift accepted after runtime capture")
			}
		})
	}
}

func TestCodexOwnedReferencesRejectChangesWithoutReadingSourceAuth(t *testing.T) {
	for _, kind := range []string{"generated_config_changed", "generated_config_writable", "auth_link_changed", "auth_target_wrong", "source_auth_changed"} {
		t.Run(kind, func(t *testing.T) {
			layout, _, source, owned := newCodexSealableFixture(t)
			link := filepath.Join(layout.Home.Path, "auth.json")
			switch kind {
			case "generated_config_changed":
				if err := os.WriteFile(filepath.Join(layout.Home.Path, "config.toml"), []byte("model=\"other\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "generated_config_writable":
				if err := os.Chmod(filepath.Join(layout.Home.Path, "config.toml"), 0644); err != nil {
					t.Fatal(err)
				}
			case "auth_link_changed", "auth_target_wrong":
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(source.Home, "auth.json")
				if kind == "auth_target_wrong" {
					target = filepath.Join(source.Home, "config.toml")
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			case "source_auth_changed":
				if err := os.Chmod(filepath.Join(source.Home, "auth.json"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "source_auth_changed" {
				if codexVerifySourceReferences(context.Background(), source) == nil {
					t.Fatal("source reference drift admitted")
				}
			} else if codexVerifyOwnedReferences(context.Background(), layout.Home, source, owned) == nil {
				t.Fatal("owned protected reference drift admitted")
			}
		})
	}
}

func TestCodexRetainedSealableObservationRechecksAfterLaterWork(t *testing.T) {
	layout, f, source, owned := newCodexSealableFixture(t)
	remaining := codexStateTotalLimit
	observation, err := codexCaptureSealableObservation(context.Background(), layout, f.binding, f.installation, source, owned, codexStateReadControl{Remaining: &remaining})
	if err != nil {
		t.Fatal(err)
	}
	before := remaining
	path := filepath.Join(layout.SQLite.Path, "state_5.sqlite-wal")
	if err := os.WriteFile(path, []byte("inserted after physical and source capture"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := codexVerifySealableObservation(context.Background(), observation); err == nil {
		t.Fatal("publisher's retained observation did not reject later fixed-slot drift")
	}
	if remaining != before {
		t.Fatal("metadata-only barrier consumed or reset byte budget")
	}
}
