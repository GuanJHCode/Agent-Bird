//go:build darwin || linux

package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStorageReaderHoldsSnapshotAndLocalGate(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.ns.afterReadOpen = func(name string) {
		if name == "manifest.json" {
			once.Do(func() { close(entered); <-release })
		}
	}
	done := make(chan error, 1)
	go func() {
		p, err := r.Read(ctx, PageCursor{}, RecordBytes)
		if err == nil && string(pageBytes(p)) != "first" {
			err = errors.New("snapshot changed")
		}
		done <- err
	}()
	<-entered
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	_, err := r.Read(deadline, PageCursor{}, 1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-reader gate %v", err)
	}
	if err = w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 5, Data: []byte("next")}); err != nil {
		t.Fatal(err)
	}
	deadline, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
	err = w.Flush(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot shared lock released early %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("lock timeout made safe retry fatal %v", err)
	}
}

func TestStorageCanceledCloseCannotReleaseActiveWriter(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("value")}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.ns.step = func(at string) error {
		if at == "chunk.stage:after-write" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- w.Flush(ctx) }()
	<-entered
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err := w.Close(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close ignored gate deadline %v", err)
	}
	other, err := OpenWriter(ctx, root, storageScope())
	if other != nil {
		_ = other.Close(ctx)
	}
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("canceled close released live writer %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStorageGenerationOverflowDoesNotMutateFiles(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	before, err := w.ns.readFile("manifest.json", ManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("value")}); err != nil {
		t.Fatal(err)
	}
	before, err = w.ns.readFile("manifest.json", ManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	w.m.Generation = ^uint64(0) - 1
	if err := w.Flush(ctx); !errors.Is(err, ErrOverflow) {
		t.Fatalf("generation wrap %v", err)
	}
	after, err := w.ns.readFile("manifest.json", ManifestBytes)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("overflow changed canonical manifest")
	}
	if err := w.Close(ctx); !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
}

func TestStoragePartialAppendLockTimeoutCannotCloseComplete(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	data := bytes.Repeat([]byte("x"), RecordBytes)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: data}); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	unlock, err := r.ns.lockUpdate(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err = w.Append(deadline, Record{SegmentID: "s", Stream: Stdout, Offset: RecordBytes, Data: data})
	cancel()
	if releaseErr := unlock(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial append did not hit lock wait %v", err)
	}
	if err := w.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partly accepted append became complete: %v", err)
	}
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatalf("partial append completeness %+v %v", p.Snapshot, err)
	}
}
