package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type filterFrozenBlob struct {
	oid, sha string
	size     int64
	filtered bool
}

// Read one proven stream slot, optionally into the sole bounded memory bridge.
func filterInspectSlot(ctx context.Context, streams FilterStreams, slot int, size int64, sha string, oidLength int, dst io.Writer) (filterFrozenBlob, error) {
	if size < 0 || size > gitfilter.MaximumBytes {
		return filterFrozenBlob{}, fail(CodeInvalidInput, "filtered blob size", nil)
	}
	object, content := filterBlobHash(size, oidLength), sha256.New()
	out := &filterBoundedWriter{dst: io.MultiWriter(dst, object, content), limit: size}
	err := streams.ReadSlot(ctx, slot, out)
	if err = errors.Join(err, out.err); err != nil {
		return filterFrozenBlob{}, err
	}
	if out.count != size || hex.EncodeToString(content.Sum(nil)) != sha {
		return filterFrozenBlob{}, fail(CodeIdentityChanged, "filtered canonical stream", nil)
	}
	return filterFrozenBlob{oid: hex.EncodeToString(object.Sum(nil)), sha: sha, size: size, filtered: true}, nil
}

func filterCleanObserved(ctx context.Context, m *FilterMaterialization, w *filterRawWorkspace, rel string) (gitfilter.Result, error) {
	err := filterReadObserved(ctx, w, rel, func(f *os.File) error { return m.streams.Load(ctx, 0, f) })
	if err != nil {
		return gitfilter.Result{}, err
	}
	clean, err := filterTransform(ctx, m.source, m.streams, "clean", 0, 1)
	want := w.owned[rel]
	if err == nil && (clean.InputSHA256 != want.sha256 || clean.InputBytes != want.identity.Size) {
		err = fail(CodeIdentityChanged, "filtered clean input", nil)
	}
	return clean, err
}

func filterRoundTripObserved(ctx context.Context, m *FilterMaterialization, w *filterRawWorkspace, rel string) (filterFrozenBlob, error) {
	clean, err := filterCleanObserved(ctx, m, w, rel)
	if err != nil {
		return filterFrozenBlob{}, err
	}
	smudge, err := filterTransform(ctx, m.source, m.streams, "smudge", 1, 2)
	if err != nil {
		return filterFrozenBlob{}, err
	}
	want := w.owned[rel]
	if smudge.InputSHA256 != clean.OutputSHA256 || smudge.InputBytes != clean.OutputBytes || smudge.OutputSHA256 != want.sha256 || smudge.OutputBytes != want.identity.Size {
		return filterFrozenBlob{}, fail(CodeInvalidInput, "filtered rendered round trip", nil)
	}
	again, err := filterTransform(ctx, m.source, m.streams, "clean", 2, 0)
	if err != nil {
		return filterFrozenBlob{}, err
	}
	if again.InputSHA256 != smudge.OutputSHA256 || again.InputBytes != smudge.OutputBytes || again.OutputSHA256 != clean.OutputSHA256 || again.OutputBytes != clean.OutputBytes {
		return filterFrozenBlob{}, fail(CodeInvalidInput, "filtered canonical round trip", nil)
	}
	return filterInspectSlot(ctx, m.streams, 1, clean.OutputBytes, clean.OutputSHA256, len(m.receipt.CommitOID), io.Discard)
}

func filterHashObserved(ctx context.Context, w *filterRawWorkspace, rel string, oidLength int, dst io.Writer) (filterFrozenBlob, error) {
	want := w.owned[rel]
	object, content := filterBlobHash(want.identity.Size, oidLength), sha256.New()
	err := filterReadObserved(ctx, w, rel, func(f *os.File) error {
		n, e := io.Copy(io.MultiWriter(dst, object, content), io.LimitReader(f, want.identity.Size+1))
		if e != nil {
			return e
		}
		if n != want.identity.Size {
			return fail(CodeIdentityChanged, "filtered raw size", nil)
		}
		return nil
	})
	sha := hex.EncodeToString(content.Sum(nil))
	if err == nil && sha != want.sha256 {
		err = fail(CodeIdentityChanged, "filtered raw digest", nil)
	}
	return filterFrozenBlob{oid: hex.EncodeToString(object.Sum(nil)), sha: sha, size: want.identity.Size}, err
}

func filterWriteObservedBlob(ctx context.Context, m *FilterMaterialization, w *filterRawWorkspace, rel string, want filterFrozenBlob) error {
	// No io.ReadAll here or in the blob runner: a single exact-capacity buffer
	// bridges the authenticated slot/file to process.Command.Stdin.
	if want.size < 0 || want.size > gitfilter.MaximumBytes {
		return fail(CodeInvalidInput, "filtered write size", nil)
	}
	buffer := bytes.NewBuffer(make([]byte, 0, int(want.size)))
	out := &filterBoundedWriter{dst: buffer, limit: want.size}
	var actual filterFrozenBlob
	var err error
	if want.filtered {
		clean, e := filterCleanObserved(ctx, m, w, rel)
		if e != nil {
			return e
		}
		if clean.OutputBytes != want.size || clean.OutputSHA256 != want.sha {
			return fail(CodeIdentityChanged, "filtered second clean", nil)
		}
		actual, err = filterInspectSlot(ctx, m.streams, 1, want.size, want.sha, len(want.oid), out)
	} else {
		actual, err = filterHashObserved(ctx, w, rel, len(want.oid), out)
	}
	if err = errors.Join(err, out.err); err != nil {
		return err
	}
	if actual != want || out.count != want.size {
		return fail(CodeIdentityChanged, "filtered write object", nil)
	}
	got, err := writeFilterBlob(ctx, m.source.repoRoot, buffer.Bytes())
	if err != nil {
		return err
	}
	if got != want.oid {
		return fail(CodeIdentityChanged, "filtered written object", nil)
	}
	return nil
}

func writeFilterBlob(ctx context.Context, dir string, body []byte) (string, error) {
	g := filterGitFromContext(ctx)
	if g == nil || int64(len(body)) > gitfilter.MaximumBytes {
		return "", fail(CodeInvalidInput, "filtered blob runner", nil)
	}
	if err := g.verifyDirectory(dir); err != nil {
		return "", err
	}
	if err := g.Verify(ctx); err != nil {
		return "", err
	}
	r, err := candidateGitBytesWithStart(ctx, dir, body, true, process.Start, "hash-object", "--no-filters", "-w", "--stdin")
	return strings.TrimSpace(r.Stdout), g.verifyAfter(err)
}
