package gitops

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

func filterBlobHash(size int64, oidLength int) hash.Hash {
	var h hash.Hash = sha1.New()
	if oidLength == 64 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	return h
}

func filterProviderFile(f *os.File) (FileIdentity, error) {
	id, err := filterRegularIdentity(f)
	if err != nil {
		return id, err
	}
	mode := os.FileMode(id.Mode)
	p := mode.Perm()
	if mode != p || (p != 0600 && p != 0644 && p != 0700 && p != 0755) || id.Size < 0 || id.Size > 16<<20 {
		return id, fail(CodeInvalidInput, "filtered provider file mode/size", nil)
	}
	return id, nil
}

// Observe only through the original workspace descriptor. This permits a
// declared regular file replacement, not adoption of a new workspace or dir.
func filterObserveChanges(ctx context.Context, w *filterRawWorkspace, allowed, filtered map[string]bool) (*filterRawWorkspace, error) {
	if err := w.verify(); err != nil {
		return nil, err
	}
	observed := *w
	observed.owned = map[string]filterOwnedEntry{}
	observed.total = 0
	newDirs, neededDirs, aliases := map[string]bool{}, map[string]bool{}, map[string]bool{}
	pathBytes := 0
	var walk func(*os.File, string) error
	walk = func(parent *os.File, prefix string) error {
		if _, err := parent.Seek(0, io.SeekStart); err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := parent.ReadDir(128)
			for _, entry := range entries {
				rel := entry.Name()
				if prefix != "" {
					rel = prefix + "/" + rel
				}
				pathBytes += len(rel)
				key := strings.ToLower(rel)
				if (rel != ".git" && !filterSafePath(rel)) || aliases[key] || len(observed.owned) >= 4096 || pathBytes > 256<<10 {
					return fail(CodeUnknownObject, "filtered change inventory", nil)
				}
				aliases[key] = true
				old, existed := w.owned[rel]
				f, err := filterOpenAt(parent, entry.Name(), unix.O_RDONLY, 0)
				if err != nil {
					return err
				}
				err = func() error {
					defer f.Close()
					st, err := f.Stat()
					if err != nil {
						return err
					}
					if st.IsDir() {
						if _, err = filterDirectoryInfo(st); err != nil {
							return err
						}
						id, err := identityFromInfo(st)
						if err != nil {
							return err
						}
						if st.Mode() != os.ModeDir|0700 || allowed[rel] || existed && !sameRemovalIdentity(id, old.identity) {
							return fail(CodeIdentityChanged, "filtered change directory", nil)
						}
						if !existed {
							ancestor := false
							for p := range allowed {
								if strings.HasPrefix(p, rel+"/") {
									ancestor = true
									break
								}
							}
							if !ancestor {
								return fail(CodeUnknownObject, "filtered unexpected directory", nil)
							}
							newDirs[rel] = true
						}
						observed.owned[rel] = filterOwnedEntry{identity: id}
						return walk(f, rel)
					}
					id, err := filterProviderFile(f)
					if err != nil {
						return err
					}
					if !allowed[rel] && (!existed || id != old.identity) {
						return fail(CodeIdentityChanged, "filtered undeclared file", nil)
					}
					if existed && os.FileMode(old.identity.Mode).IsDir() {
						return fail(CodeIdentityChanged, "filtered replaced directory", nil)
					}
					h := sha256.New()
					n, err := io.Copy(h, io.LimitReader(f, id.Size+1))
					if err != nil {
						return err
					}
					after, err := filterProviderFile(f)
					sha := hex.EncodeToString(h.Sum(nil))
					if err != nil || after != id || n != id.Size || !allowed[rel] && sha != old.sha256 {
						return fail(CodeIdentityChanged, "filtered changed content", err)
					}
					observed.owned[rel] = filterOwnedEntry{id, sha}
					if rel != ".git" {
						observed.total += n
						if observed.total > filterWorkspaceMaximumBytes {
							return fail(CodeInvalidInput, "filtered rendered total", nil)
						}
						for d := path.Dir(rel); d != "."; d = path.Dir(d) {
							neededDirs[d] = true
						}
					}
					return nil
				}()
				if err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk(w.root, ""); err != nil {
		return nil, err
	}
	for p, old := range w.owned {
		if _, present := observed.owned[p]; !present && (!allowed[p] || filtered[p] || os.FileMode(old.identity.Mode).IsDir()) {
			return nil, fail(CodeIdentityChanged, "filtered missing entry", nil)
		}
	}
	for d := range newDirs {
		if !neededDirs[d] {
			return nil, fail(CodeUnknownObject, "filtered empty directory", nil)
		}
	}
	// A fresh traversal also checks held/named continuity after replacements.
	if _, err := observed.inventory(ctx); err != nil {
		return nil, err
	}
	return &observed, nil
}

// filterReadObserved validates every parent against the observed directory set
// and binds the streamed bytes to one exact regular-file identity and digest.
func filterReadObserved(ctx context.Context, w *filterRawWorkspace, rel string, consume func(*os.File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.verify(); err != nil {
		return err
	}
	parts := strings.Split(rel, "/")
	parent := w.root
	opened := []*os.File{}
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			opened[i].Close()
		}
	}()
	for i, part := range parts[:len(parts)-1] {
		f, err := filterOpenAt(parent, part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		opened = append(opened, f)
		st, err := f.Stat()
		if err != nil {
			return err
		}
		id, err := identityFromInfo(st)
		want, ok := w.owned[strings.Join(parts[:i+1], "/")]
		if err != nil || !ok || !sameRemovalIdentity(id, want.identity) {
			return fail(CodeIdentityChanged, "filtered read parent", err)
		}
		if _, err = filterDirectoryInfo(st); err != nil {
			return err
		}
		parent = f
	}
	f, err := filterOpenAt(parent, parts[len(parts)-1], unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	id, err := filterProviderFile(f)
	want, ok := w.owned[rel]
	if err != nil || !ok || id != want.identity {
		return fail(CodeIdentityChanged, "filtered read file", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, id.Size+1))
	if err != nil || n != id.Size || hex.EncodeToString(h.Sum(nil)) != want.sha256 {
		return fail(CodeIdentityChanged, "filtered read digest", err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = consume(f); err != nil {
		return err
	}
	after, err := filterProviderFile(f)
	if err != nil || after != id {
		return fail(CodeIdentityChanged, "filtered read changed", err)
	}
	return w.verify()
}
