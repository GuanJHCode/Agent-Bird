package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCodexSourceFixture(t *testing.T, config string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("SYNTHETIC_AUTH_NEVER_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCodexSourceReferencesProjectConfigAndKeepAuthMetadataOnly(t *testing.T) {
	for _, config := range []string{"", "model=\"gpt-fixed\"\nmodel_reasoning_effort=\"future-effort\"\n"} {
		name := "missing_config"
		if config != "" {
			name = "present_config"
		}
		t.Run(name, func(t *testing.T) {
			home := newCodexSourceFixture(t, config)
			refs, projection, err := codexCaptureSourceReferences(context.Background(), home, codexSourceControl{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(refs)
			if err != nil {
				t.Fatal(err)
			}
			if projection == nil || refs.Home != home || refs.Config.Present != (config != "") || refs.Auth.Nlink != 1 || refs.Auth.Size != int64(len("SYNTHETIC_AUTH_NEVER_READ")) || strings.Contains(string(raw), "SYNTHETIC_AUTH") || strings.Contains(string(projection.ConfigTOML()), "SYNTHETIC_AUTH") {
				t.Fatal("projection/reference scope or secrecy broken")
			}
			if config != "" {
				sum := sha256.Sum256([]byte(config))
				if refs.Config.SHA256 != hex.EncodeToString(sum[:]) || !strings.Contains(string(projection.ConfigTOML()), "future-effort") {
					t.Fatal("original admitted defaults not preserved")
				}
			}
		})
	}
}

func TestCodexSourceReferencesRejectUnsafeAndLateDrift(t *testing.T) {
	for _, kind := range []string{"auth_missing", "auth_symlink", "auth_hardlink", "auth_writable", "config_symlink", "config_too_large", "unsupported_source", "root_writable", "late_config_replace", "late_config_insert", "late_auth_replace", "late_root_replace", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			config := "model=\"gpt-fixed\"\n"
			if kind == "late_config_insert" {
				config = ""
			}
			home := newCodexSourceFixture(t, config)
			auth := filepath.Join(home, "auth.json")
			configPath := filepath.Join(home, "config.toml")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "auth_missing":
				if err := os.Remove(auth); err != nil {
					t.Fatal(err)
				}
			case "auth_symlink":
				if err := os.Remove(auth); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(configPath, auth); err != nil {
					t.Fatal(err)
				}
			case "auth_hardlink":
				if err := os.Link(auth, filepath.Join(home, "extra")); err != nil {
					t.Fatal(err)
				}
			case "auth_writable":
				if err := os.Chmod(auth, 0666); err != nil {
					t.Fatal(err)
				}
			case "config_symlink":
				if err := os.Remove(configPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(auth, configPath); err != nil {
					t.Fatal(err)
				}
			case "config_too_large":
				f, err := os.OpenFile(configPath, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate(1<<20 + 1); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			case "unsupported_source":
				if err := os.WriteFile(configPath, []byte("unknown_source=\"refuse\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "root_writable":
				if err := os.Chmod(home, 0777); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(home, 0700)
			case "canceled":
				cancel()
			}
			reached := false
			_, _, err := codexCaptureSourceReferences(ctx, home, codexSourceControl{BeforeVerify: func(name string, fd int) {
				if name != "auth.json" || !strings.HasPrefix(kind, "late_") {
					return
				}
				reached = true
				if fd < 0 {
					t.Fatal("missing original auth FD")
				}
				path := configPath
				body := []byte(config)
				switch kind {
				case "late_config_insert":
					body = []byte("model=\"gpt-fixed\"\n")
				case "late_auth_replace":
					path = auth
					body = []byte("SYNTHETIC_AUTH_NEVER_READ")
				case "late_root_replace":
					if err := os.Rename(home, home+"-original"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(home + "-original") })
					if err := os.Mkdir(home, 0700); err != nil {
						t.Fatal(err)
					}
					return
				}
				tmp := filepath.Join(home, "replacement")
				if err := os.WriteFile(tmp, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}})
			if err == nil {
				t.Fatal("unsafe or drifted source reference accepted")
			}
			if strings.HasPrefix(kind, "late_") && !reached {
				t.Fatal("actual original auth-FD checkpoint not reached")
			}
		})
	}
}

func TestCodexSourceReferencesUseOriginalFDsAndDetectOfflineChanges(t *testing.T) {
	home := newCodexSourceFixture(t, "model=\"gpt-fixed\"\n")
	if err := os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	}
	var fds []int
	refs, _, err := codexCaptureSourceReferences(context.Background(), home, codexSourceControl{BeforeVerify: func(name string, fd int) {
		if fd < 0 {
			t.Fatal("expected present original FD")
		}
		fds = append(fds, fd)
		if name == "auth.json" {
			offset, err := unix.Seek(fd, 0, io.SeekCurrent)
			if err != nil || offset != 0 {
				t.Fatal("auth FD was consumed")
			}
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 2 {
		t.Fatal("source config was not read exactly once with auth metadata FD")
	}
	for _, fd := range fds {
		var stat unix.Stat_t
		if !errors.Is(unix.Fstat(fd, &stat), unix.EBADF) {
			t.Fatal("original reference FD leaked")
		}
	}
	if err := codexVerifySourceReferences(context.Background(), refs); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(home, "auth.json")
	tmp := filepath.Join(home, "replacement")
	if err := os.WriteFile(tmp, []byte("SYNTHETIC_AUTH_NEVER_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, auth); err != nil {
		t.Fatal(err)
	}
	if err := codexVerifySourceReferences(context.Background(), refs); err == nil {
		t.Fatal("same-content offline source auth replacement accepted")
	}
}
