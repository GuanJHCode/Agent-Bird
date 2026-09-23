package workspaceread

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readerFixture(t *testing.T) (string, *Reader) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"README.md":          "Fixture\n",
		"src/calc.py":        "def add(a, b):\n    return a - b\n",
		".git/config":        "private git metadata\n",
		".codex/config.toml": "private provider configuration\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return root, r
}

func TestListAndReadRepositoryContextWithContinuation(t *testing.T) {
	_, r := readerFixture(t)
	ctx := context.Background()
	first, err := r.List(ctx, ".", "", 1)
	if err != nil || !reflect.DeepEqual(first.Paths, []string{"README.md"}) || !first.Truncated || first.NextAfter != "README.md" {
		t.Fatalf("first file page: %+v %v", first, err)
	}
	second, err := r.List(ctx, ".", first.NextAfter, 10)
	if err != nil || !reflect.DeepEqual(second.Paths, []string{"src/calc.py"}) || second.Truncated {
		t.Fatalf("second file page: %+v %v", second, err)
	}
	file, err := r.Read(ctx, "src/calc.py", 2, 1)
	if err != nil || !reflect.DeepEqual(file.Lines, []string{"    return a - b"}) || file.StartLine != 2 || file.Truncated {
		t.Fatalf("file context: %+v %v", file, err)
	}
}

func TestSearchFindsCodeWithoutReturningProviderMetadata(t *testing.T) {
	_, r := readerFixture(t)
	found, err := r.Search(context.Background(), `return\s+a`, ".", "", 20)
	if err != nil || len(found.Matches) != 1 || found.Matches[0].Path != "src/calc.py" || found.Matches[0].Line != 2 || found.Matches[0].Text != "    return a - b" {
		t.Fatalf("search context: %+v %v", found, err)
	}
	private, err := r.Search(context.Background(), "private", ".", "", 20)
	if err != nil || len(private.Matches) != 0 {
		t.Fatalf("protected metadata escaped: %+v %v", private, err)
	}
}

func TestReaderRejectsTraversalProtectedMetadataAndLinkedFiles(t *testing.T) {
	root, r := readerFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside value"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, makeLink := range []func() error{
		func() error { return os.Symlink(secret, filepath.Join(root, "file-link")) },
		func() error { return os.Symlink(outside, filepath.Join(root, "dir-link")) },
		func() error { return os.Link(secret, filepath.Join(root, "hard-link")) },
	} {
		if err := makeLink(); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"../secret", "/etc/passwd", "src/../../secret", "src//calc.py", "src/./calc.py", ".git/config", ".GIT/config", ".codex/config.toml", ".CODEX/config.toml", "file-link", "dir-link/secret", "hard-link", "src\\calc.py", "src/\x00file"} {
		t.Run(path, func(t *testing.T) {
			if _, err := r.Read(context.Background(), path, 1, 20); err == nil {
				t.Fatal("untrusted path returned content")
			}
		})
	}
	listed, err := r.List(context.Background(), ".", "", 100)
	if err != nil || !reflect.DeepEqual(listed.Paths, []string{"README.md", "src/calc.py"}) {
		t.Fatalf("linked files entered listing: %+v %v", listed, err)
	}
}

func TestProtectedComponentsRejectCaseAliasesOnEveryFilesystem(t *testing.T) {
	for _, path := range []string{".GIT/config", "src/.GiT/config", ".CODEX/config.toml", "src/.CoDeX/config.toml", ".AGENT-BIRD/state", "src/.Agent-Bird/state"} {
		if validPath(path, false) || validPath(path, true) {
			t.Errorf("protected case alias accepted: %s", path)
		}
	}
}

func TestNativePathValidationRejectsEscapesLinksAndRootWrites(t *testing.T) {
	root, reader := readerFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "hard")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "..", ".GIT/config", "linked/outside", "linked/new", "hard", "src/../new"} {
		if err := reader.CheckNativePath(context.Background(), path, true, false); err == nil {
			t.Errorf("untrusted native edit path accepted: %s", path)
		}
	}
	for _, path := range []string{"README.md", "src/new.py", "newdir/nested/file.py"} {
		if err := reader.CheckNativePath(context.Background(), path, true, false); err != nil {
			t.Errorf("valid isolated edit rejected: %s %v", path, err)
		}
	}
	if err := reader.CheckNativePath(context.Background(), ".", true, true); err != nil {
		t.Fatal("root metadata rejected:", err)
	}
}

func TestReaderDetectsWorkspaceReplacementAndCancellation(t *testing.T) {
	root, r := readerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Read(ctx, "README.md", 1, 10); err == nil {
		t.Fatal("cancelled read continued")
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(root) })
	if _, err := r.List(context.Background(), ".", "", 10); err == nil {
		t.Fatal("replaced workspace accepted")
	}
}

func TestReaderBoundsInputAndReturnsEditedContent(t *testing.T) {
	root, r := readerFixture(t)
	path := filepath.Join(root, "src/calc.py")
	if err := os.WriteFile(path, []byte("def add(a, b):\n    return a + b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := r.Read(context.Background(), "src/calc.py", 2, 1)
	if err != nil || !reflect.DeepEqual(file.Lines, []string{"    return a + b"}) {
		t.Fatal("reader returned stale content", err)
	}
	for _, text := range []string{"binary\x00data", strings.Repeat("x", 1024*1024+1)} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Read(context.Background(), "src/calc.py", 1, 10); err == nil {
			t.Fatal("unsafe file size/type accepted")
		}
	}
	for _, query := range []string{"", "[", strings.Repeat("x", 513)} {
		if _, err := r.Search(context.Background(), query, ".", "", 20); err == nil {
			t.Fatal("invalid search input accepted")
		}
	}
	if _, err := r.Read(context.Background(), "README.md", 0, 10); err == nil {
		t.Fatal("invalid line range accepted")
	}
}
