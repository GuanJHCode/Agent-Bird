//go:build darwin || linux

package codextools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func testBroker(t *testing.T, options Options) (*Broker, string) {
	t.Helper()
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := Open(root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, root
}

func writableOptions() Options {
	return Options{Writable: true, MaxWrittenBytes: 64, BeforeWrite: func(context.Context, WriteIntent) error { return nil }, AfterWrite: func(context.Context, WriteReceipt) error { return nil }}
}

func TestBrokerReadCreateAndFailClosedReplacement(t *testing.T) {
	b, _ := testBroker(t, writableOptions())
	first := []byte("first")
	created, err := b.Write(context.Background(), "note.txt", "", first)
	if err != nil || created.SHA256 != digest(first) || created.Bytes != int64(len(first)) {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	first[0] = 'X'
	read, got, err := b.Read(context.Background(), "note.txt")
	if err != nil || string(read) != "first" || got != created.SHA256 {
		t.Fatalf("read=%q sha=%q err=%v", read, got, err)
	}
	if _, err = b.Write(context.Background(), "note.txt", created.SHA256, []byte("second")); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement without exclusive lease err=%v", err)
	}
	if _, err = b.Write(context.Background(), "note.txt", "", []byte("third")); !errors.Is(err, ErrConflict) {
		t.Fatalf("create-only overwrite err=%v", err)
	}
	read, got, err = b.Read(context.Background(), "note.txt")
	if err != nil || string(read) != "first" || got != created.SHA256 {
		t.Fatalf("conflict changed original: read=%q sha=%q err=%v", read, got, err)
	}
}

func TestBrokerWriteGuardsAndUnknownReceipt(t *testing.T) {
	if _, err := Open(t.TempDir(), Options{Writable: true, MaxWrittenBytes: 1}); err == nil {
		t.Fatal("writable broker accepted missing callbacks")
	}
	readonly, _ := testBroker(t, Options{})
	if _, err := readonly.Write(context.Background(), "no.txt", "", []byte("x")); !errors.Is(err, ErrReadonly) {
		t.Fatalf("readonly err=%v", err)
	}
	before := writableOptions()
	before.BeforeWrite = func(context.Context, WriteIntent) error { return errors.New("intent rejected") }
	blocked, root := testBroker(t, before)
	if _, err := blocked.Write(context.Background(), "blocked.txt", "", []byte("x")); err == nil {
		t.Fatal("before-write failure accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, "blocked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("before-write failure changed file: %v", err)
	}
	options := writableOptions()
	options.AfterWrite = func(context.Context, WriteReceipt) error { return errors.New("durable receipt failed") }
	b, _ := testBroker(t, options)
	if _, err := b.Write(context.Background(), "unknown.txt", "", []byte("x")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("after failure err=%v", err)
	}
	if _, err := b.Write(context.Background(), "other.txt", "", []byte("x")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown broker accepted second write: %v", err)
	}
}

func TestBrokerListIsBoundedAndAnchored(t *testing.T) {
	b, root := testBroker(t, writableOptions())
	if err := os.Mkdir(filepath.Join(root, "listed"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(context.Background(), "listed/a", "", []byte("a")); err != nil {
		t.Fatal(err)
	}
	names, err := b.List(context.Background(), "listed")
	if err != nil || len(names) != 1 || names[0] != "a" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	again, err := b.List(context.Background(), "listed")
	if err != nil || !reflect.DeepEqual(names, again) || len(again) == 0 {
		t.Fatalf("repeated list=%v err=%v", again, err)
	}
	for i := 0; i < 257; i++ {
		if err := os.WriteFile(filepath.Join(root, "listed", fmt.Sprintf("n-%03d", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = b.List(context.Background(), "listed"); err == nil {
		t.Fatal("unbounded list accepted")
	}
	if err := os.Mkdir(filepath.Join(root, ".GIT"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = b.List(context.Background(), ".GIT"); err == nil {
		t.Fatal("case-insensitive protected path accepted")
	}
	rootNames, err := b.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range rootNames {
		if name == ".GIT" {
			t.Fatal("protected entry leaked")
		}
	}
	if _, err = b.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerConcurrentCreateKeepsWinningContent(t *testing.T) {
	b, root := testBroker(t, writableOptions())
	second, err := Open(root, writableOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	start := make(chan struct{})
	type outcome struct {
		content string
		receipt WriteReceipt
		err     error
	}
	results := make(chan outcome, 2)
	for _, item := range []struct {
		broker  *Broker
		content string
	}{{b, "first"}, {second, "second"}} {
		go func(item struct {
			broker  *Broker
			content string
		}) { <-start; r, err := item.broker.Write(context.Background(), "race.txt", "", []byte(item.content)); results <- outcome{item.content, r, err} }(item)
	}
	close(start)
	var winner outcome
	for range 2 {
		out := <-results
		if out.err == nil {
			winner = out
			continue
		}
		if !errors.Is(out.err, ErrConflict) {
			t.Fatalf("create err=%v", out.err)
		}
	}
	if winner.content == "" {
		t.Fatal("no create winner")
	}
	data, got, err := b.Read(context.Background(), "race.txt")
	if err != nil || string(data) != winner.content || got != winner.receipt.SHA256 {
		t.Fatalf("winner not retained: data=%q sha=%q receipt=%+v err=%v", data, got, winner.receipt, err)
	}
}

type unknownSysInfo struct{}

func (unknownSysInfo) Name() string       { return "unknown" }
func (unknownSysInfo) Size() int64        { return 0 }
func (unknownSysInfo) Mode() os.FileMode  { return 0 }
func (unknownSysInfo) ModTime() time.Time { return time.Time{} }
func (unknownSysInfo) IsDir() bool        { return false }
func (unknownSysInfo) Sys() any           { return "not-a-stat" }

func TestFileIdentityRejectsNilAndUnknownSys(t *testing.T) {
	if _, ok := fileIdentity(nil); ok {
		t.Fatal("nil identity accepted")
	}
	if _, ok := fileIdentity(unknownSysInfo{}); ok {
		t.Fatal("unknown Sys identity accepted")
	}
}

func TestBrokerRejectsTraversalLinksAndBudget(t *testing.T) {
	b, root := testBroker(t, writableOptions())
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(context.Background(), "dir/nested.txt", "", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Read(context.Background(), "../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := b.List(context.Background(), ".git"); err == nil {
		t.Fatal("protected git accepted")
	}
	if err := os.Symlink("dir", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.List(context.Background(), "linked"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Link(filepath.Join(root, "dir", "nested.txt"), filepath.Join(root, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Read(context.Background(), "hard.txt"); err == nil {
		t.Fatal("hardlink accepted")
	}
	limited := writableOptions()
	limited.MaxWrittenBytes = 3
	budget, _ := testBroker(t, limited)
	if _, err := budget.Write(context.Background(), "a", "", []byte("123")); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Write(context.Background(), "b", "", []byte("4")); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget err=%v", err)
	}
}

func TestBrokerConcurrentBudgetAndFrozenContent(t *testing.T) {
	options := writableOptions()
	options.MaxWrittenBytes = 2
	b, _ := testBroker(t, options)
	content := []byte("x")
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for _, name := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := b.Write(context.Background(), name, "", content)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrBudget) {
			t.Fatalf("concurrent err=%v", err)
		}
	}
	if ok != 2 {
		t.Fatalf("writes=%d want 2", ok)
	}
}
