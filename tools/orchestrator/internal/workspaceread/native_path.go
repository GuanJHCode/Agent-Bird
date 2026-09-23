package workspaceread

import (
	"context"
	"os"
	"strings"
)

// CheckNativePath validates a native file operation against the same anchored
// root as context reads. A missing tail may be created by native apply_patch;
// every existing component must still be an owned, unlinked repository node.
func (r *Reader) CheckNativePath(ctx context.Context, path string, allowMissing, allowDirectory bool) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !validPath(path, allowDirectory) {
		return CodeError("workspace_path_rejected")
	}
	if err := r.check(ctx); err != nil {
		return err
	}
	if path == "." {
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) > 32 {
		return CodeError("workspace_path_rejected")
	}
	parent := r.root
	var held []*os.Root
	defer func() {
		for _, child := range held {
			_ = child.Close()
		}
	}()
	for index, part := range parts {
		info, err := parent.Lstat(part)
		if os.IsNotExist(err) && allowMissing {
			return r.check(ctx)
		}
		if err != nil {
			return CodeError("workspace_path_unavailable")
		}
		last := index == len(parts)-1
		if info.IsDir() {
			if last && !allowDirectory {
				return CodeError("workspace_file_untrusted")
			}
			child, err := childRoot(parent, part, info)
			if err != nil {
				return err
			}
			held = append(held, child)
			parent = child
		} else if !last || !ordinaryFile(info) || info.Size() > maxFileBytes {
			return CodeError("workspace_file_untrusted")
		}
	}
	return r.check(ctx)
}
