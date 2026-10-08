//go:build !windows

package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrConfigGenerationPending = errors.New("config_generation_pending")
var ErrConfigGenerationConflict = errors.New("config_generation_conflict")

type generationOwner struct {
	path, scope         string
	root, config, owner *os.File
	dirs                map[string]*os.File
}

var generationDirectories = []string{"generations", "requests", "preparations", "uses", "abandoned"}

// generationOwnerExists distinguishes a missing owner (which publication may
// initialize) from a damaged existing owner missing a required child. It is
// deliberately read-only so rejection cannot repair or mutate foreign state.
func generationOwnerExists(root, scope string) (bool, error) {
	r, c, err := generationOwnedRoot(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer r.Close()
	defer c.Close()
	owner, err := generationDirectory(c, scope, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, owner.Close()
}

func openGenerationOwner(root, scope string, create bool) (*generationOwner, error) {
	if !generationHash(scope) {
		return nil, generationInvalid()
	}
	r, c, err := generationOwnedRoot(root, create)
	if err != nil {
		return nil, err
	}
	g := &generationOwner{path: root, scope: scope, root: r, config: c, dirs: map[string]*os.File{}}
	owner, err := generationDirectory(c, scope, false)
	if errors.Is(err, os.ErrNotExist) && create {
		names, e := generationNames(c, generationCapacity)
		if e != nil || len(names) >= generationCapacity {
			g.close()
			return nil, errors.New("config_generation_capacity")
		}
		owner, err = generationDirectory(c, scope, true)
	}
	if err != nil {
		g.close()
		return nil, err
	}
	g.owner = owner
	for _, name := range generationDirectories {
		g.dirs[name], err = generationDirectory(owner, name, create)
		if (name == "uses" || name == "abandoned") && !create && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			g.close()
			return nil, err
		}
	}
	if err = g.validate(); err != nil {
		g.close()
		return nil, err
	}
	return g, nil
}

func (g *generationOwner) close() {
	for _, f := range g.dirs {
		if f != nil {
			f.Close()
		}
	}
	for _, f := range []*os.File{g.owner, g.config, g.root} {
		if f != nil {
			f.Close()
		}
	}
}

func (g *generationOwner) validate() error {
	for _, item := range []struct {
		f    *os.File
		path string
	}{{g.root, g.path}, {g.config, filepath.Join(g.path, "owned-config")}, {g.owner, filepath.Join(g.path, "owned-config", g.scope)}} {
		if item.f == nil || publicationSamePath(item.f, item.path) != nil {
			return generationInvalid()
		}
		info, err := item.f.Stat()
		if err != nil {
			return err
		}
		id, err := publicationDirectory(info)
		if err != nil || id.Mode != 0700 {
			return generationInvalid()
		}
	}
	for name, f := range g.dirs {
		if f == nil {
			continue
		}
		if publicationSamePath(f, filepath.Join(g.path, "owned-config", g.scope, name)) != nil {
			return generationInvalid()
		}
	}
	return nil
}

func generationRecordNames(dir *os.File) ([]string, error) {
	names, err := generationNames(dir, generationCapacity*2)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, name := range names {
		base := strings.TrimPrefix(name, ".pending-")
		if !strings.HasSuffix(base, ".json") || !generationHash(strings.TrimSuffix(base, ".json")) {
			return nil, generationInvalid()
		}
		seen[base] = true
	}
	if len(seen) > generationCapacity {
		return nil, errors.New("config_generation_capacity")
	}
	return names, nil
}

func generationOriginalRecord(dir *os.File, name string, limit int) ([]byte, publicationFileIdentity, bool, error) {
	final, finalID, finalErr := publicationReadAt(dir, name, int64(limit))
	pending, pendingID, pendingErr := publicationReadAt(dir, ".pending-"+name, int64(limit))
	if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return nil, finalID, false, finalErr
	}
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return nil, pendingID, false, pendingErr
	}
	// Two physical records are ambiguous even if their bytes happen to match.
	// Neither a malformed short write nor a competing original may be hidden.
	if finalErr == nil && pendingErr == nil {
		return nil, finalID, false, ErrConfigGenerationPending
	}
	if finalErr == nil {
		return final, finalID, true, nil
	}
	if pendingErr == nil {
		return pending, pendingID, true, nil
	}
	return nil, finalID, false, nil
}

func (g *generationOwner) writeRecord(dir *os.File, name string, raw []byte, limit int) error {
	return g.writeOriginalRecord(dir, name, raw, limit, true)
}

// Completed request history is direct-addressed, never enumerated as a live
// reference. Only live generation/preparation directories use the 64 bound.
func (g *generationOwner) writeOriginalRecord(dir *os.File, name string, raw []byte, limit int, bounded bool) error {
	if len(raw) == 0 || len(raw) > limit || g.validate() != nil {
		return generationInvalid()
	}
	body, id, exists, err := generationOriginalRecord(dir, name, limit)
	if err != nil {
		return err
	}
	if exists && !bytes.Equal(body, raw) {
		return ErrConfigGenerationConflict
	}
	if _, _, err := publicationReadAt(dir, name, int64(limit)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bounded {
		names, err := generationRecordNames(dir)
		if err != nil {
			return err
		}
		unique := map[string]bool{}
		for _, n := range names {
			unique[strings.TrimPrefix(n, ".pending-")] = true
		}
		if !unique[name] && len(unique) >= generationCapacity {
			return errors.New("config_generation_capacity")
		}
	}
	pending := ".pending-" + name
	if !exists {
		id, err = publicationWriteAt(dir, pending, raw)
		if err == nil {
			err = dir.Sync()
		}
		if err != nil {
			return err
		}
	}
	if err = g.validate(); err != nil {
		return err
	}
	if err = publicationRenameNoReplace(int(dir.Fd()), pending, int(dir.Fd()), name); err != nil {
		return err
	}
	if err = dir.Sync(); err != nil {
		return err
	}
	body, current, err := publicationReadAt(dir, name, int64(limit))
	if err != nil || current != id || !bytes.Equal(body, raw) {
		return ErrConfigGenerationConflict
	}
	return g.validate()
}

func (g *generationOwner) historyDirectory(key string, create bool) (*os.File, error) {
	if !generationHash(key) {
		return nil, generationInvalid()
	}
	dir, err := generationDirectory(g.dirs["requests"], key[:2], create)
	if err != nil {
		return nil, err
	}
	if g.validate() != nil || publicationSamePath(dir, filepath.Join(g.path, "owned-config", g.scope, "requests", key[:2])) != nil {
		dir.Close()
		return nil, generationInvalid()
	}
	return dir, nil
}

func (g *generationOwner) removeRecord(dir *os.File, name string, raw []byte, limit int) error {
	if g.validate() != nil {
		return generationInvalid()
	}
	body, _, err := publicationReadAt(dir, name, int64(limit))
	if err != nil || !bytes.Equal(body, raw) {
		return ErrConfigGenerationConflict
	}
	if err = unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return err
	}
	if err = dir.Sync(); err != nil {
		return err
	}
	return g.validate()
}
