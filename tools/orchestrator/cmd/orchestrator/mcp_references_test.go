package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func shortReviewFile(t *testing.T, handle, token string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "m1v_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("not a compact review reference: len=%d err=%v", len(token), err)
	}
	return filepath.Join(filepath.Dir(handle), "mcp-reviews", hex.EncodeToString(raw)+".json")
}

func TestMCPCompactReferencesRestartAndLegacyCompatibility(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	if len(x.route) != 79 || !strings.HasPrefix(x.route, "m1r_") {
		t.Fatalf("route still requires long random transcription: %d", len(x.route))
	}
	token := x.review(t)
	if len(token) != 47 || !strings.HasPrefix(token, "m1v_") {
		t.Fatalf("review still requires long random transcription: %d", len(token))
	}
	f, err := newMCPFacade(x.f.path)
	if err != nil {
		t.Fatal(err)
	}
	route, _, _, err := f.resolveRoute(x.route)
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.resolveReview(token, route)
	if err != nil {
		t.Fatal(err)
	}
	oldRoute, err := f.seal("route", route)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.resolveRoute(oldRoute); err != nil {
		t.Fatalf("old route lost compatibility: %v", err)
	}
	oldReview, err := f.seal("review", review)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.resolveReview(oldReview, route); err != nil || got != review {
		t.Fatalf("old full tuple lost compatibility: %+v %v", got, err)
	}
}

func TestMCPCompactReviewIsImmutableAndBusinessReadOnly(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	beforeRows := compactBusinessDatabase(t, x.state)
	before, err := x.db.TaskSnapshot(x.ctx, "edit")
	if err != nil {
		t.Fatal(err)
	}
	token := x.review(t)
	path := shortReviewFile(t, x.handle, token)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("private review record: %v %v", info, err)
	}
	for i := 0; i < 5; i++ {
		if again := x.review(t); again != token {
			t.Fatal("same frozen tuple minted another reference")
		}
	}
	afterInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("repeated inspect rewrote immutable metadata")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("repeated inspect grows records: %d", records)
	}
	after, err := x.db.TaskSnapshot(x.ctx, "edit")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("inspect changed business state: %v", err)
	}
	if afterRows := compactBusinessDatabase(t, x.state); !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("inspect modified business database rows")
	}
}

func compactBusinessDatabase(t *testing.T, state string) map[string]string {
	t.Helper()
	path := (&url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	result := map[string]string{}
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		var values [][]any
		for rows.Next() {
			v := make([]any, len(columns))
			pointers := make([]any, len(v))
			for i := range v {
				pointers[i] = &v[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			values = append(values, v)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		body, _ := json.Marshal(values)
		digest := sha256.Sum256(body)
		result[table] = hex.EncodeToString(digest[:])
	}
	return result
}

func compactReviewTuple(t *testing.T, x *mcpBusinessFixture) (mcpRoute, mcpReview, string) {
	t.Helper()
	token := x.review(t)
	route, _, _, err := x.f.resolveRoute(x.route)
	if err != nil {
		t.Fatal(err)
	}
	review, err := x.f.resolveReview(token, route)
	if err != nil {
		t.Fatal(err)
	}
	return route, review, token
}

func TestMCPCompactReferenceTamperAndScope(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	route, _, review := compactReviewTuple(t, x)
	for _, token := range []string{x.route[:len(x.route)-1], x.route + "=", review, "m1r_" + strings.Repeat("_", 75)} {
		if _, _, _, err := x.f.resolveRoute(token); err == nil {
			t.Fatal("malformed/cross-kind route accepted")
		}
	}
	for _, token := range []string{review[:len(review)-1], review + "=", x.route, "m1v_" + strings.Repeat("_", 43)} {
		if _, err := x.f.resolveReview(token, route); err == nil {
			t.Fatal("malformed/cross-kind review accepted")
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(x.route, "m1r_"))
	for _, index := range []int{0, 12, len(raw) - 1} {
		changed := append([]byte(nil), raw...)
		changed[index] ^= 1
		if _, _, _, err := x.f.resolveRoute("m1r_" + base64.RawURLEncoding.EncodeToString(changed)); err == nil {
			t.Fatal("changed coordinate/signature accepted")
		}
	}
	// Same-key namespaces still have distinct route/review MAC domains.
	request := filepath.Join(x.root, "other-configure.json")
	securityWriteJSON(t, request, mcpConfigureRequest{Version: 1, Root: filepath.Join(x.root, "other-config"), StateDir: x.state, Workspace: x.root, Providers: map[string]string{}})
	if err := configureMCP(request, io.Discard); err != nil {
		t.Fatal(err)
	}
	other, err := newMCPFacade(filepath.Join(x.root, "other-config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(x.state, "mcp-keys", other.config.Namespace, "token-key"), x.f.key, 0600); err != nil {
		t.Fatal(err)
	}
	other, err = newMCPFacade(other.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = other.resolveRoute(x.route); err == nil {
		t.Fatal("cross-namespace route accepted")
	}
	if _, err = other.resolveReview(review, route); err == nil {
		t.Fatal("cross-namespace review accepted")
	}
}

func TestMCPCompactRouteRevalidatesPrivateMetadata(t *testing.T) {
	for _, mutation := range []string{"run", "tasks", "state", "owner", "reservation_run", "reservation_missing", "reservation_public", "reservation_symlink", "reservation_hardlink", "control_missing"} {
		t.Run(mutation, func(t *testing.T) {
			x := newMCPBusinessFixture(t, false)
			var handle taskHandle
			if err := mcpReadPrivate(x.handle, mcpConfigMaxBytes, &handle); err != nil {
				t.Fatal(err)
			}
			rp := filepath.Join(filepath.Dir(x.handle), "reservation.json")
			var reservation dispatchReservation
			if err := mcpReadPrivate(rp, mcpConfigMaxBytes, &reservation); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "run":
				handle.RunID = "other"
			case "tasks":
				handle.TaskIDs = []string{"other"}
			case "state":
				handle.StateDir = filepath.Dir(x.state)
			case "owner":
				reservation.ControllerThread = "other"
			case "reservation_run":
				reservation.RunID = "other"
			case "reservation_missing":
				if err := os.Remove(rp); err != nil {
					t.Fatal(err)
				}
			case "reservation_public":
				if err := os.Chmod(rp, 0644); err != nil {
					t.Fatal(err)
				}
			case "reservation_symlink":
				if err := os.Rename(rp, rp+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(rp+".real", rp); err != nil {
					t.Fatal(err)
				}
			case "reservation_hardlink":
				if err := os.Link(rp, rp+".link"); err != nil {
					t.Fatal(err)
				}
			case "control_missing":
				handle.ControlFile = ""
			}
			if mutation == "run" || mutation == "tasks" || mutation == "state" || mutation == "control_missing" {
				securityWriteJSON(t, x.handle, handle)
			}
			if mutation == "owner" || mutation == "reservation_run" {
				securityWriteJSON(t, rp, reservation)
			}
			if _, _, _, err := x.f.resolveRoute(x.route); err == nil {
				t.Fatal("changed routing metadata accepted")
			}
		})
	}
}

func TestMCPCompactReviewPrivateRecords(t *testing.T) {
	for _, attack := range []string{"tamper", "whitespace", "public", "symlink", "hardlink", "parent_public", "parent_symlink", "oversize", "missing"} {
		t.Run(attack, func(t *testing.T) {
			x := newMCPBusinessFixture(t, true)
			route, value, token := compactReviewTuple(t, x)
			path := shortReviewFile(t, x.handle, token)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch attack {
			case "tamper":
				value.Binding.CandidateOID = strings.Repeat("f", 40)
				securityWriteJSON(t, path, value)
			case "whitespace":
				if err = os.WriteFile(path, append(original, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "public":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Rename(path, path+".real")
				if err == nil {
					err = os.Symlink(path+".real", path)
				}
			case "hardlink":
				err = os.Link(path, path+".link")
			case "parent_public":
				err = os.Chmod(filepath.Dir(path), 0755)
			case "parent_symlink":
				parent := filepath.Dir(path)
				err = os.Rename(parent, parent+".real")
				if err == nil {
					err = os.Symlink(parent+".real", parent)
				}
			case "oversize":
				err = os.WriteFile(path, []byte(strings.Repeat("x", 16*1024+1)), 0600)
			case "missing":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = x.f.resolveReview(token, route); err == nil {
				t.Fatal("untrusted review record accepted")
			}
			if attack == "missing" {
				if again := x.review(t); again != token {
					t.Fatal("current tuple recreation changed reference")
				}
				if _, err = x.f.resolveReview(token, route); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMCPCompactReviewConcurrentDedupAndQuota(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	route, review, token := compactReviewTuple(t, x)
	var wait sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := x.f.reviewReference(route, review.TaskID, review.Binding)
			if err == nil && got != token {
				err = fmt.Errorf("same tuple produced new reference")
			}
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	path := shortReviewFile(t, x.handle, token)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Fatal("record hardlinked")
	}
	for i := 1; i < 64; i++ {
		binding := review.Binding
		binding.EventID = fmt.Sprintf("distinct-event-%d", i)
		if _, err := x.f.reviewReference(route, review.TaskID, binding); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	binding := review.Binding
	binding.EventID = "sixty-fifth"
	if _, err := x.f.reviewReference(route, review.TaskID, binding); errorCode(err) != "mcp_review_reference_capacity" {
		t.Fatalf("quota error: %v", err)
	}
	if got, err := x.f.reviewReference(route, review.TaskID, review.Binding); err != nil || got != token {
		t.Fatalf("quota invalidated existing reference: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 65 {
		t.Fatalf("unexpected files after rejected write: %d", len(entries))
	}
	for _, entry := range entries {
		st, err := entry.Info()
		if err != nil || st.Size() > 16*1024 {
			t.Fatalf("unbounded record: %v", err)
		}
	}
}

func TestMCPCompactReviewLockAndRecordLimits(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	route, review, token := compactReviewTuple(t, x)
	path := shortReviewFile(t, x.handle, token)
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = x.f.reviewReference(route, review.TaskID, review.Binding)
	if errorCode(err) != "mcp_reference_storage_busy" || time.Since(start) > 3*time.Second {
		t.Fatalf("unbounded/bypassed lock: %v", err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	binding := review.Binding
	binding.EventID = strings.Repeat("x", 16*1024)
	if _, err = x.f.reviewReference(route, review.TaskID, binding); errorCode(err) != "mcp_review_reference_too_large" {
		t.Fatalf("oversized write: %v", err)
	}
}
