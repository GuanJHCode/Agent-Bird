//go:build darwin || linux

package artifacts

import (
	"context"
	"errors"
	"path/filepath"
)

// PrepareHostSlotNamespace is called by original dispatch, after storage
// admission and before publishing a grant. It creates only the three fixed
// owner-only namespace directories, never object/lock files. No writer lease is
// acquired, so the caller may retain its dispatch transaction. Cleanup must use
// this saved identity; it must never call this function to adopt a namespace.
func PrepareHostSlotNamespace(ctx context.Context, stateRoot, hostID string) (p HostSlotNamespaceProof, err error) {
	if !token(hostID) || ctx.Err() != nil {
		return p, errors.Join(ErrUnsafe, ctx.Err())
	}
	c, err := openAnchor(stateRoot, false)
	if err != nil {
		return p, err
	}
	defer func() { err = errors.Join(err, c.close()) }()
	for _, name := range []string{"host-spool", hostID, "managed-artifacts"} {
		if err = ctx.Err(); err != nil {
			return p, err
		}
		if err = c.child(name, true); err != nil {
			return p, err
		}
	}
	if err = c.check(); err != nil {
		return p, err
	}
	p = HostSlotNamespaceProof{Version: 1, RootPath: filepath.Join(stateRoot, "host-spool", hostID), RootDevice: uint64(c.ids[2].Dev), RootInode: c.ids[2].Ino, ParentDevice: uint64(c.ids[3].Dev), ParentInode: c.ids[3].Ino}
	return p, ctx.Err()
}
