package coordinator

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// This live publication owns only the file it created with O_EXCL. It retains
// its handles until Store reports a committed, rolled-back or uncertain outcome.
// A file alone never authorizes a grant or a cold cleanup operation.
type localOwnerPublication struct {
	stateDir, name, path string
	authority            store.StoreRootAuthority
	root, owners         *os.Root
	file                 *os.File
	created              os.FileInfo
	expected             []byte
}

func ownerGrantNode(info os.FileInfo, mode os.FileMode, directory bool) bool {
	if info == nil || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && (directory || st.Nlink == 1)
}

func (p *localOwnerPublication) validateDirectory() error {
	invalid := store.CodeError("owner_path_untrusted")
	if p.authority.Validate() != nil || p.root == nil || p.owners == nil {
		return invalid
	}
	rootInfo, err := p.root.Stat(".")
	if err != nil || !ownerGrantNode(rootInfo, 0700, true) {
		return invalid
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	expectedRoot := p.authority.Nodes()
	if !ok || uint64(rootStat.Dev) != expectedRoot.StateDevice || uint64(rootStat.Ino) != expectedRoot.StateInode {
		return invalid
	}
	named, err := p.root.Lstat("owners")
	if err != nil || !ownerGrantNode(named, 0700, true) {
		return invalid
	}
	held, err := p.owners.Stat(".")
	if err != nil || !ownerGrantNode(held, 0700, true) || !os.SameFile(named, held) {
		return invalid
	}
	return nil
}

func (p *localOwnerPublication) validateFile(complete bool) error {
	if err := p.validateDirectory(); err != nil {
		return err
	}
	invalid := store.CodeError("owner_capability_publication_uncertain")
	if p.file == nil || p.created == nil {
		return invalid
	}
	held, err := p.file.Stat()
	if err != nil || !ownerGrantNode(held, 0600, false) || !os.SameFile(held, p.created) || held.Size() < 0 || held.Size() > int64(len(p.expected)) {
		return invalid
	}
	named, err := p.owners.Lstat(p.name)
	if err != nil || !ownerGrantNode(named, 0600, false) || !os.SameFile(named, held) || named.Size() != held.Size() || !named.ModTime().Equal(held.ModTime()) {
		return invalid
	}
	if complete && held.Size() != int64(len(p.expected)) {
		return invalid
	}
	body := make([]byte, held.Size())
	if len(body) > 0 {
		if _, err = p.file.ReadAt(body, 0); err != nil {
			return invalid
		}
	}
	after, err := p.file.Stat()
	if err != nil || !ownerGrantNode(after, 0600, false) || !os.SameFile(after, held) || after.Size() != held.Size() || !after.ModTime().Equal(held.ModTime()) || !bytes.Equal(body, p.expected[:len(body)]) {
		return invalid
	}
	return nil
}

func (p *localOwnerPublication) publish(g store.OwnerGrant, authority store.StoreRootAuthority) error {
	p.authority = authority
	if authority.Nodes().StateRoot != p.stateDir || authority.Validate() != nil {
		return store.CodeError("owner_path_untrusted")
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > 8192 {
		return store.CodeError("owner_capability_too_large")
	}
	p.expected = raw
	p.root, err = os.OpenRoot(p.stateDir)
	if err != nil {
		return err
	}
	if err = p.root.Mkdir("owners", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := p.root.Lstat("owners")
	if err != nil || !ownerGrantNode(info, 0700, true) {
		return store.CodeError("owner_path_untrusted")
	}
	p.owners, err = p.root.OpenRoot("owners")
	if err != nil {
		return err
	}
	if err = p.validateDirectory(); err != nil {
		return err
	}
	p.name = g.ID + ".json"
	p.path = filepath.Join(p.stateDir, "owners", p.name)
	p.file, err = p.owners.OpenFile(p.name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	p.created, err = p.file.Stat()
	if err != nil {
		return err
	}
	if !ownerGrantNode(p.created, 0600, false) {
		return store.CodeError("owner_capability_publication_uncertain")
	}
	n, err := p.file.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	if err = p.file.Sync(); err != nil {
		return err
	}
	if err = p.validateFile(true); err != nil {
		return err
	}
	dir, err := p.owners.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return err
	}
	// The first owners-directory creation must also be durable in state root.
	state, err := p.root.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(state.Sync(), state.Close())
	if err != nil {
		return err
	}
	return p.validateFile(true)
}

func (p *localOwnerPublication) removeAfterRollback() error {
	if p.file == nil {
		return nil
	}
	// Caller invokes this only after its explicit live DB rollback succeeded.
	// Identity drift or any incomplete observation preserves the file.
	if err := p.validateFile(false); err != nil {
		return err
	}
	if err := p.owners.Remove(p.name); err != nil {
		return err
	}
	dir, err := p.owners.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (p *localOwnerPublication) close() {
	if p.file != nil {
		_ = p.file.Close()
	}
	if p.owners != nil {
		_ = p.owners.Close()
	}
	if p.root != nil {
		_ = p.root.Close()
	}
}
