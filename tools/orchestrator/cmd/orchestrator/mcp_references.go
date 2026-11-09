package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	mcpCompactRoutePrefix  = "m1r_"
	mcpCompactReviewPrefix = "m1v_"
	mcpReviewRecordLimit   = 16 * 1024
	mcpReviewRecordCount   = 64
)

func (f *mcpFacade) referenceMAC(domain string, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, f.key)
	for _, part := range append([][]byte{[]byte(domain), []byte(f.config.Namespace)}, parts...) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = mac.Write(size[:])
		_, _ = mac.Write(part)
	}
	return mac.Sum(nil)
}

// Coordinates are the existing two 24-hex dispatch directory names. The MAC
// also commits to the full reconstructed route, never just a mutable path.
func (f *mcpFacade) routeCoordinates(path string) ([]byte, bool) {
	rel, err := filepath.Rel(filepath.Join(f.config.StateDir, "dispatch"), path)
	parts := strings.Split(rel, string(filepath.Separator))
	if err != nil || len(parts) != 3 || parts[2] != "handle.json" {
		return nil, false
	}
	var raw []byte
	for _, part := range parts[:2] {
		decoded, err := hex.DecodeString(part)
		if err != nil || len(decoded) != 12 || hex.EncodeToString(decoded) != part {
			return nil, false
		}
		raw = append(raw, decoded...)
	}
	return raw, true
}

func (f *mcpFacade) reconstructRoute(path string) (mcpRoute, error) {
	var empty mcpRoute
	if !f.routePathAllowed(path) {
		return empty, codeError("mcp_route_scope_mismatch")
	}
	if err := privateHandleParent(path); err != nil {
		return empty, err
	}
	var handle taskHandle
	var reservation dispatchReservation
	if err := mcpReadPrivate(path, mcpConfigMaxBytes, &handle); err != nil {
		return empty, err
	}
	if err := mcpReadPrivate(filepath.Join(filepath.Dir(path), "reservation.json"), mcpConfigMaxBytes, &reservation); err != nil {
		return empty, err
	}
	if handle.Version != 1 || handle.StateDir != f.config.StateDir || handle.RunID == "" || len(handle.TaskIDs) == 0 || reservation.Version != 1 || reservation.RunID != handle.RunID || reservation.ControllerThread == "" {
		return empty, codeError("mcp_route_scope_mismatch")
	}
	return f.routeValue(path, reservation.ControllerThread, handle), nil
}

func (f *mcpFacade) compactRoute(route mcpRoute, coordinates []byte) (string, error) {
	current, err := f.reconstructRoute(route.Handle)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(route)
	if err != nil {
		return "", err
	}
	actual, err := json.Marshal(current)
	if err != nil || !bytes.Equal(body, actual) {
		return "", codeError("mcp_route_scope_mismatch")
	}
	raw := append(append([]byte(nil), coordinates...), f.referenceMAC("mcp-short-route-v1", coordinates, body)...)
	return mcpCompactRoutePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (f *mcpFacade) openCompactRoute(token string) (mcpRoute, error) {
	var empty mcpRoute
	if len(token) != 79 {
		return empty, codeError("invalid_mcp_reference")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, mcpCompactRoutePrefix))
	if err != nil || len(raw) != 24+sha256.Size {
		return empty, codeError("invalid_mcp_reference")
	}
	path := filepath.Join(f.config.StateDir, "dispatch", hex.EncodeToString(raw[:12]), hex.EncodeToString(raw[12:24]), "handle.json")
	route, err := f.reconstructRoute(path)
	if err != nil {
		return empty, err
	}
	body, err := json.Marshal(route)
	if err != nil || !hmac.Equal(raw[24:], f.referenceMAC("mcp-short-route-v1", raw[:24], body)) {
		return empty, codeError("invalid_mcp_reference")
	}
	return route, nil
}

// These records wrap an immutable review tuple. They are not business state or
// authority: every use still resolves the real route/control and Store guards.
// Descriptors anchor all creation/reads beneath the validated request directory.
type mcpReferenceDirectory struct {
	request, records *os.File
	requestPath      string
	recordsPath      string
}

func namedPrivateDirectory(path string, file *os.File) bool {
	opened, err := file.Stat()
	named, namedErr := os.Lstat(path)
	canonical, canonicalErr := filepath.EvalSymlinks(path)
	return err == nil && namedErr == nil && canonicalErr == nil && canonical == path && opened.IsDir() && opened.Mode().Perm() == 0700 && owned(opened) && named.IsDir() && named.Mode().Perm() == 0700 && owned(named) && named.Mode()&os.ModeSymlink == 0 && os.SameFile(opened, named)
}

func (d *mcpReferenceDirectory) named() bool {
	return namedPrivateDirectory(d.requestPath, d.request) && namedPrivateDirectory(d.recordsPath, d.records)
}

func (d *mcpReferenceDirectory) close() {
	_ = d.records.Close()
	_ = d.request.Close()
}

func (f *mcpFacade) openReferenceDirectory(route mcpRoute, create bool) (*mcpReferenceDirectory, error) {
	if !f.routePathAllowed(route.Handle) {
		return nil, codeError("invalid_mcp_reference")
	}
	if err := privateHandleParent(route.Handle); err != nil {
		return nil, err
	}
	requestPath := filepath.Dir(route.Handle)
	fd, err := unix.Open(requestPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, codeError("mcp_reference_storage_untrusted")
	}
	request := os.NewFile(uintptr(fd), requestPath)
	if !namedPrivateDirectory(requestPath, request) {
		_ = request.Close()
		return nil, codeError("mcp_reference_storage_untrusted")
	}
	if create {
		if err := unix.Mkdirat(fd, "mcp-reviews", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			_ = request.Close()
			return nil, codeError("mcp_reference_storage_unavailable")
		}
	}
	recordsPath := filepath.Join(requestPath, "mcp-reviews")
	recordFD, err := unix.Openat(fd, "mcp-reviews", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = request.Close()
		return nil, codeError("invalid_mcp_reference")
	}
	dir := &mcpReferenceDirectory{request: request, records: os.NewFile(uintptr(recordFD), recordsPath), requestPath: requestPath, recordsPath: recordsPath}
	if !dir.named() {
		dir.close()
		return nil, codeError("mcp_reference_storage_untrusted")
	}
	return dir, nil
}

func privateReferenceFile(file *os.File, directory int, name string, max int64) (os.FileInfo, bool) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) || info.Size() < 0 || info.Size() > max {
		return nil, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	var named unix.Stat_t
	if !ok || st.Nlink != 1 || unix.Fstatat(directory, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || named.Dev != st.Dev || named.Ino != st.Ino || named.Nlink != 1 || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0777 != 0600 || named.Uid != uint32(os.Geteuid()) || named.Size != info.Size() {
		return nil, false
	}
	return info, true
}

func (d *mcpReferenceDirectory) read(name string) ([]byte, error) {
	fd, err := unix.Openat(int(d.records.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, codeError("invalid_mcp_reference")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	before, ok := privateReferenceFile(file, int(d.records.Fd()), name, mcpReviewRecordLimit)
	if !ok || before.Size() < 1 {
		return nil, codeError("invalid_mcp_reference")
	}
	body, err := io.ReadAll(io.LimitReader(file, mcpReviewRecordLimit+1))
	after, valid := privateReferenceFile(file, int(d.records.Fd()), name, mcpReviewRecordLimit)
	if err != nil || !valid || int64(len(body)) != before.Size() || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || !d.named() {
		return nil, codeError("invalid_mcp_reference")
	}
	return body, nil
}

func (d *mcpReferenceDirectory) lock() (*os.File, error) {
	fd, err := unix.Openat(int(d.records.Fd()), ".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, codeError("mcp_reference_storage_untrusted")
	}
	file := os.NewFile(uintptr(fd), ".lock")
	deadline := time.Now().Add(time.Second)
	for {
		info, ok := privateReferenceFile(file, int(d.records.Fd()), ".lock", 0)
		if !ok || info.Size() != 0 || !d.named() {
			_ = file.Close()
			return nil, codeError("mcp_reference_storage_untrusted")
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if _, ok := privateReferenceFile(file, int(d.records.Fd()), ".lock", 0); ok && d.named() {
				return file, nil
			}
			_ = file.Close()
			return nil, codeError("mcp_reference_storage_untrusted")
		}
		if (!errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR)) || !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, codeError("mcp_reference_storage_busy")
		}
		time.Sleep(time.Millisecond)
	}
}

func (d *mcpReferenceDirectory) capacity() error {
	entries, err := d.records.ReadDir(mcpReviewRecordCount + 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return codeError("mcp_reference_storage_unavailable")
	}
	count := 0
	var total int64
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		name := entry.Name()
		id, err := hex.DecodeString(strings.TrimSuffix(name, ".json"))
		var st unix.Stat_t
		if err != nil || len(id) != sha256.Size || name != hex.EncodeToString(id)+".json" || unix.Fstatat(int(d.records.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size < 0 || st.Size > mcpReviewRecordLimit {
			return codeError("mcp_reference_storage_untrusted")
		}
		count++
		total += st.Size
	}
	if count >= mcpReviewRecordCount || total > mcpReviewRecordCount*mcpReviewRecordLimit || !d.named() {
		return codeError("mcp_review_reference_capacity")
	}
	return nil
}

func (f *mcpFacade) compactReview(route mcpRoute, review mcpReview) (string, error) {
	body, err := json.Marshal(review)
	if err != nil {
		return "", err
	}
	record := append(append([]byte(nil), body...), '\n')
	if len(record) > mcpReviewRecordLimit {
		return "", codeError("mcp_review_reference_too_large")
	}
	mac := f.referenceMAC("mcp-short-review-v1", body)
	token := mcpCompactReviewPrefix + base64.RawURLEncoding.EncodeToString(mac)
	name := hex.EncodeToString(mac) + ".json"
	dir, err := f.openReferenceDirectory(route, true)
	if err != nil {
		return "", err
	}
	defer dir.close()
	lock, err := dir.lock()
	if err != nil {
		return "", err
	}
	defer lock.Close()
	var st unix.Stat_t
	if err = unix.Fstatat(int(dir.records.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		existing, err := dir.read(name)
		if err != nil || !bytes.Equal(existing, record) {
			return "", codeError("invalid_mcp_reference")
		}
		return token, nil
	} else if !errors.Is(err, unix.ENOENT) {
		return "", codeError("mcp_reference_storage_untrusted")
	}
	if err = dir.capacity(); err != nil {
		return "", err
	}
	fd, err := unix.Openat(int(dir.records.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", codeError("mcp_reference_storage_unavailable")
	}
	file := os.NewFile(uintptr(fd), name)
	// No replacement or automatic deletion. A failed/partial publication remains
	// bounded and fails closed; it never becomes an accepted review reference.
	_, err = file.Write(record)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = dir.records.Sync()
	}
	if err != nil {
		return "", codeError("mcp_reference_storage_unavailable")
	}
	actual, err := dir.read(name)
	if err != nil || !bytes.Equal(actual, record) {
		return "", codeError("invalid_mcp_reference")
	}
	return token, nil
}

func (f *mcpFacade) openCompactReview(token string, route mcpRoute) (mcpReview, error) {
	var review mcpReview
	if len(token) != 47 {
		return review, codeError("invalid_mcp_reference")
	}
	mac, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, mcpCompactReviewPrefix))
	if err != nil || len(mac) != sha256.Size {
		return review, codeError("invalid_mcp_reference")
	}
	dir, err := f.openReferenceDirectory(route, false)
	if err != nil {
		return review, err
	}
	defer dir.close()
	body, err := dir.read(hex.EncodeToString(mac) + ".json")
	if err != nil || decode(body, &review) != nil {
		return review, codeError("invalid_mcp_reference")
	}
	canonical, err := json.Marshal(review)
	if err != nil || !bytes.Equal(body, append(append([]byte(nil), canonical...), '\n')) || !hmac.Equal(mac, f.referenceMAC("mcp-short-review-v1", canonical)) {
		return review, codeError("invalid_mcp_reference")
	}
	return review, nil
}
