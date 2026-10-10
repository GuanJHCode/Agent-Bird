package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOpenFilterGitRoutesPinnedContextAndDetectsDrift(t *testing.T) {
	repository, _ := repo(t)
	clean := filterTool(t, "clean")
	smudge := filterTool(t, "smudge")
	gitPath := filterGitPath(t)
	manifest := filterManifestFor(t, gitPath, clean, smudge)
	manifest = configureFilter(t, repository, manifest)
	contextGit, err := OpenFilterGit(context.Background(), repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer contextGit.Close()
	if _, err := run(contextGit.Context(context.Background()), repository, "rev-parse", "--git-dir"); err != nil {
		t.Fatalf("routed git: %v", err)
	}
	if contextGit.SHA256() == "" {
		t.Fatal("missing live context digest")
	}
	git(t, repository, "config", "core.filemode", "false")
	if err := contextGit.Verify(context.Background()); err == nil {
		t.Fatal("configuration drift accepted")
	}
	git(t, repository, "config", "core.filemode", "true")
	if err := contextGit.Verify(context.Background()); err != nil {
		t.Fatal("config was not restored before the independent attr check", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("a.txt filter=other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := contextGit.Verify(context.Background()); err == nil {
		t.Fatal("attribute drift accepted")
	}
}

func TestOpenFilterGitRejectsUnlistedFilterWithoutExecutingIt(t *testing.T) {
	repository, _ := repo(t)
	clean := filterTool(t, "clean")
	smudge := filterTool(t, "smudge")
	gitPath := filterGitPath(t)
	manifest := filterManifestFor(t, gitPath, clean, smudge)
	manifest = configureFilter(t, repository, manifest)
	sentinel := filepath.Join(t.TempDir(), "executed")
	git(t, repository, "config", "filter.evil.clean", "touch "+sentinel)
	if _, err := OpenFilterGit(context.Background(), repository, manifest); err == nil {
		t.Fatal("unlisted filter accepted")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("malicious filter executed: %v", err)
	}
}

func TestOpenFilterGitRoutesRecordsAndRejectsTrackedForeignAndClosedContexts(t *testing.T) {
	repository, head := repo(t)
	manifest := configureFilter(t, repository, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
	g, err := OpenFilterGit(context.Background(), repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	ctx := g.Context(context.Background())
	foreign, _ := repo(t)
	path := t.TempDir()
	sentinel := filepath.Join(path, "bare-git")
	if err := os.WriteFile(filepath.Join(path, "git"), []byte("#!/bin/sh\n/usr/bin/touch "+sentinel+"\nexit 90\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	if _, err := gitRecords(ctx, repository, "rev-parse", "--git-dir"); err != nil {
		t.Fatalf("records bypassed pinned git: %v", err)
	}
	var streamed bytes.Buffer
	if err := candidateGitStream(ctx, repository, &streamed, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(streamed.String()) != head {
		t.Fatalf("stream bypassed pinned Git: %q %v", streamed.String(), err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("bare git executed: %v", err)
	}
	if _, err := run(context.WithValue(ctx, trackedIntegrationRunnerKey{}, true), repository, "rev-parse", "HEAD"); err == nil {
		t.Fatal("tracked filter context accepted")
	}
	if err := candidateGitStream(context.WithValue(ctx, trackedIntegrationRunnerKey{}, true), repository, &streamed, "rev-parse", "HEAD"); err == nil {
		t.Fatal("tracked stream accepted")
	}
	if _, err := run(ctx, foreign, "rev-parse", "HEAD"); err == nil {
		t.Fatal("foreign repository accepted")
	}
	if err := candidateGitStream(ctx, foreign, &streamed, "rev-parse", "HEAD"); err == nil {
		t.Fatal("foreign stream accepted")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, repository, "rev-parse", "HEAD"); err == nil {
		t.Fatal("closed context reused")
	}
	if err := candidateGitStream(ctx, repository, &streamed, "rev-parse", "HEAD"); err == nil {
		t.Fatal("closed stream accepted")
	}
}

func TestOpenFilterGitRejectsUnboundSourcesAndRetainsUncertainty(t *testing.T) {
	for _, mode := range []string{"script", "replaced_open_source", "info_attributes", "ignored_attributes", "post_error"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "script" {
				p := filepath.Join(t.TempDir(), "script")
				if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0500); err != nil {
					t.Fatal(err)
				}
				if _, err := pinFilterFile(p, filterFileSHA256(t, p), 32<<20); err == nil {
					t.Fatal("non-native tool accepted")
				}
				return
			}
			if mode == "replaced_open_source" {
				p := filterTool(t, "native")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				replacement := p + ".new"
				if err = os.WriteFile(replacement, b, 0500); err != nil {
					t.Fatal(err)
				}
				if _, err = pinFilterFileWithOpenHook(p, filterFileSHA256(t, p), 32<<20, func() error { return os.Rename(replacement, p) }); err == nil {
					t.Fatal("replacement inode adopted after pin open")
				}
				return
			}
			repository, _ := repo(t)
			m := configureFilter(t, repository, filterManifestFor(t, filterGitPath(t), filterTool(t, "clean"), filterTool(t, "smudge")))
			if mode == "post_error" {
				g, err := OpenFilterGit(context.Background(), repository, m)
				if err != nil {
					t.Fatal(err)
				}
				g.Close()
				if err = g.verifyAfter(process.ErrProcessTreeUnknown); !errors.Is(err, process.ErrProcessTreeUnknown) {
					t.Fatalf("post verification erased original process uncertainty: %v", err)
				}
				return
			}
			if mode == "info_attributes" {
				if err := os.WriteFile(filepath.Join(repository, ".git", "info", "attributes"), []byte("a.txt filter=sealed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("hidden/\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(repository, "hidden"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repository, "hidden", ".gitattributes"), []byte("* filter=sealed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			g, err := OpenFilterGit(context.Background(), repository, m)
			if g != nil {
				g.Close()
			}
			if err == nil {
				t.Fatal("unbound attributes source accepted")
			}
		})
	}
}

func filterGitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func filterTool(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	b, err := os.ReadFile("/bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0500); err != nil {
		t.Fatal(err)
	}
	return p
}

func filterManifestFor(t *testing.T, gitPath, clean, smudge string) contract.GitFilterManifestV1 {
	t.Helper()
	gitHash := filterFileSHA256(t, gitPath)
	cleanHash := filterFileSHA256(t, clean)
	smudgeHash := filterFileSHA256(t, smudge)
	m := contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"a.txt"}, Git: contract.GitFilterGitPin{Path: gitPath, Version: git(t, t.TempDir(), "--version"), SHA256: gitHash}, Clean: contract.GitFilterToolPin{Path: clean, SHA256: cleanHash, Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: smudge, SHA256: smudgeHash, Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}}
	m.ConfigSHA256 = FilterConfigurationSHA256(m)
	return m
}

func configureFilter(t *testing.T, repository string, manifest contract.GitFilterManifestV1) contract.GitFilterManifestV1 {
	t.Helper()
	git(t, repository, "config", "filter."+manifest.DriverID+".clean", filterShellCommand(manifest.Clean))
	git(t, repository, "config", "filter."+manifest.DriverID+".smudge", filterShellCommand(manifest.Smudge))
	git(t, repository, "config", "filter."+manifest.DriverID+".required", "false")
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("a.txt filter="+manifest.DriverID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Attributes = []contract.GitFilterAttributePin{{Path: ".gitattributes", SHA256: filterFileSHA256(t, filepath.Join(repository, ".gitattributes"))}}
	return manifest
}

func filterFileSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
