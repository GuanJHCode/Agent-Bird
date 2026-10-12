//go:build !windows

package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type preparedInstallRequest struct {
	request             installRequest
	digest              string
	payloadDigest       string
	source              *os.File
	binaryParent        *os.File
	binaryBase          string
	observed            map[string]publicationFileIdentity
	observedDirectories map[string]publicationDirectoryIdentity
	binaryIdentity      publicationFileIdentity
}

func publicationDigest(v any) string {
	body, _ := json.Marshal(v)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func prepareInstallRequest(options InstallOptions) (*preparedInstallRequest, error) {
	p := &preparedInstallRequest{}
	var err error
	p.source, err = publicationOpenRoot(options.SourceRoot)
	if err != nil {
		return nil, err
	}
	tree, err := publicationScan(p.source)
	if err != nil {
		p.close()
		return nil, err
	}
	p.request.Payload, p.observed = tree.Files, tree.FileIDs
	p.request.Directories, p.observedDirectories = tree.Directories, tree.DirectoryIDs
	p.binaryParent, err = publicationOpenRoot(filepath.Dir(options.BinaryPath))
	if err != nil {
		p.close()
		return nil, err
	}
	p.binaryBase = filepath.Base(options.BinaryPath)
	p.request.Binary, p.binaryIdentity, err = publicationHash(p.binaryParent, p.binaryBase, installMaxBinaryBytes)
	if err != nil {
		p.close()
		return nil, err
	}
	p.request.Binary.Path = options.BinaryPath
	p.request.Version, p.request.SourceRoot, p.request.BinaryPath = options.Version, options.SourceRoot, options.BinaryPath
	p.request.DestinationRoot, p.request.DataRoot = options.DestinationRoot, options.DataRoot
	if _, err = installExpectedPayload(p.request); err != nil {
		p.close()
		return nil, err
	}
	body, err := json.Marshal(p.request)
	if err != nil || len(body) > installMaxRecord {
		p.close()
		return nil, errors.New("install_payload_limit")
	}
	p.digest = publicationDigest(p.request)
	expected, _ := installExpectedPayload(p.request)
	directories, _ := installExpectedDirectories(p.request)
	p.payloadDigest = publicationDigest(struct {
		Files       []installPayloadFile
		Directories []installPayloadDirectory
	}{expected, directories})
	return p, nil
}

func (p *preparedInstallRequest) close() {
	if p.source != nil {
		p.source.Close()
	}
	if p.binaryParent != nil {
		p.binaryParent.Close()
	}
}

func installCopiedMode(file installPayloadFile) uint32 {
	if strings.HasPrefix(file.Path, "runtime/g0/") && strings.HasSuffix(file.Path, ".py") && path.Base(file.Path) != "projectproxy_launchd_entrypoint.py" {
		return 0644
	}
	if file.Mode&0111 != 0 {
		return 0700
	}
	return 0600
}

func installExpectedPayload(request installRequest) ([]installPayloadFile, error) {
	if !versionPattern.MatchString(request.Version) || len(request.Version) > 128 || len(request.Payload) == 0 || len(request.Payload) > installMaxFiles || request.Binary.Path != request.BinaryPath || request.Binary.Size < 0 || request.Binary.Size > installMaxBinaryBytes || request.Binary.Mode > 0777 || request.Binary.Mode&0022 != 0 || !validHexDigest(request.Binary.SHA256) {
		return nil, errors.New("install_request_invalid")
	}
	for _, s := range []string{request.SourceRoot, request.BinaryPath, request.DestinationRoot, request.DataRoot} {
		if !filepath.IsAbs(s) || filepath.Clean(s) != s || len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return nil, errors.New("install_request_invalid")
		}
	}
	if _, err := installExpectedDirectories(request); err != nil {
		return nil, err
	}
	var total int64
	files := make([]installPayloadFile, 0, len(request.Payload)+1)
	packaged := false
	previous := ""
	for _, file := range request.Payload {
		if !publicationRelative(file.Path) || file.Path == "manifest.json" || file.Path <= previous || file.Size < 0 || file.Mode > 0777 || file.Mode&0022 != 0 || !validHexDigest(file.SHA256) || file.Size > installMaxBytes-total {
			return nil, errors.New("install_payload_limit")
		}
		previous = file.Path
		total += file.Size
		if file.Path == "bin/codex-orchestrator" {
			if file.Mode != 0700 || file.Size > installMaxBinaryBytes || file.Size != request.Binary.Size || file.SHA256 != request.Binary.SHA256 {
				return nil, errors.New("packaged_binary_mismatch")
			}
			packaged = true
		}
		file.Mode = installCopiedMode(file)
		files = append(files, file)
	}
	if !packaged {
		if len(files) >= installMaxFiles || request.Binary.Size > installMaxBytes-total {
			return nil, errors.New("install_payload_limit")
		}
		file := request.Binary
		file.Path = "bin/codex-orchestrator"
		file.Mode = 0700
		files = append(files, file)
	}
	// The extra binary is counted only when its bytes are actually copied.
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].Path < files[j-1].Path; j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	// The generated sealed manifest is also one regular file in the final tree.
	if len(files) >= installMaxFiles {
		return nil, errors.New("install_payload_limit")
	}
	return files, nil
}

func installExpectedDirectories(request installRequest) ([]installPayloadDirectory, error) {
	if len(request.Directories) > installMaxFiles {
		return nil, errors.New("install_payload_limit")
	}
	directories := map[string]bool{}
	previous := ""
	for _, directory := range request.Directories {
		if !publicationRelative(directory.Path) || strings.Count(directory.Path, "/") > 64 || directory.Path <= previous || directory.Mode > 0777 || directory.Mode&0022 != 0 {
			return nil, errors.New("install_request_invalid")
		}
		previous = directory.Path
		directories[directory.Path] = true
	}
	for _, directory := range request.Directories {
		if parent := path.Dir(directory.Path); parent != "." && !directories[parent] {
			return nil, errors.New("install_request_invalid")
		}
	}
	for _, file := range request.Payload {
		if directories[file.Path] {
			return nil, errors.New("install_request_invalid")
		}
		if parent := path.Dir(file.Path); parent != "." && !directories[parent] {
			return nil, errors.New("install_request_invalid")
		}
	}
	// A separately supplied binary adds exactly this one owned directory if the
	// source did not already describe it. Empty source directories remain described.
	directories["bin"] = true
	if len(directories) > installMaxFiles {
		return nil, errors.New("install_payload_limit")
	}
	result := make([]installPayloadDirectory, 0, len(directories))
	for name := range directories {
		result = append(result, installPayloadDirectory{name, 0700})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (p *preparedInstallRequest) verifyStable() error {
	if err := publicationSamePath(p.source, p.request.SourceRoot); err != nil {
		return err
	}
	if err := publicationSamePath(p.binaryParent, filepath.Dir(p.request.BinaryPath)); err != nil {
		return err
	}
	tree, err := publicationScan(p.source)
	if err != nil || !reflect.DeepEqual(tree.Files, p.request.Payload) || !reflect.DeepEqual(tree.FileIDs, p.observed) || !reflect.DeepEqual(tree.Directories, p.request.Directories) || !reflect.DeepEqual(tree.DirectoryIDs, p.observedDirectories) {
		return errors.New("install_source_changed")
	}
	binary, id, err := publicationHash(p.binaryParent, p.binaryBase, installMaxBinaryBytes)
	binary.Path = p.request.BinaryPath
	if err != nil || binary != p.request.Binary || id != p.binaryIdentity {
		return errors.New("install_source_changed")
	}
	return nil
}

func publicationCopyParent(root *os.File, relative string) (*os.File, string, func(), error) {
	if !publicationRelative(relative) {
		return nil, "", func() {}, errors.New("install_path_invalid")
	}
	parts := strings.Split(relative, "/")
	dir := root
	var opened []*os.File
	closeAll := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			opened[i].Close()
		}
	}
	for _, part := range parts[:len(parts)-1] {
		if err := unix.Mkdirat(int(dir.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			closeAll()
			return nil, "", func() {}, err
		}
		child, err := publicationOpen(dir, part, true)
		if err != nil {
			closeAll()
			return nil, "", func() {}, err
		}
		opened = append(opened, child)
		info, err := child.Stat()
		if err != nil {
			closeAll()
			return nil, "", func() {}, err
		}
		id, err := publicationDirectory(info)
		if err != nil || id.Mode != 0700 {
			closeAll()
			return nil, "", func() {}, errors.New("install_directory_unsafe")
		}
		dir = child
	}
	return dir, parts[len(parts)-1], closeAll, nil
}

func (p *preparedInstallRequest) copyTo(stage *os.File) error {
	expected, _ := installExpectedPayload(p.request)
	directories, _ := installExpectedDirectories(p.request)
	for _, directory := range directories {
		parent, _, closeParent, err := publicationCopyParent(stage, directory.Path+"/.directory")
		if err != nil {
			return err
		}
		_ = parent
		closeParent()
	}
	var copied int64
	for _, file := range expected {
		sourceParent, base, closeSource, err := openRelativeParent(p.source, file.Path)
		original, found := p.observed[file.Path]
		if file.Path == "bin/codex-orchestrator" && !found {
			sourceParent, base, closeSource, err = p.binaryParent, p.binaryBase, func() {}, nil
			original = p.binaryIdentity
		}
		if err != nil {
			return err
		}
		input, err := publicationOpen(sourceParent, base, false)
		if err != nil {
			closeSource()
			return err
		}
		info, err := input.Stat()
		if err != nil {
			input.Close()
			closeSource()
			return err
		}
		id, err := publicationFile(info)
		if err != nil || id != original {
			input.Close()
			closeSource()
			return errors.New("install_source_changed")
		}
		parent, name, closeOutput, err := publicationCopyParent(stage, file.Path)
		if err != nil {
			input.Close()
			closeSource()
			return err
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(file.Mode))
		if err != nil {
			closeOutput()
			input.Close()
			closeSource()
			return err
		}
		output := os.NewFile(uintptr(fd), name)
		h := sha256.New()
		limit := file.Size
		if limit > installMaxBytes-copied {
			limit = installMaxBytes - copied
		}
		if err = output.Chmod(os.FileMode(file.Mode)); err == nil {
			var n int64
			n, err = publicationCopyBounded(io.MultiWriter(output, h), input, limit)
			copied += n
			if err == nil && (n != file.Size || copied > installMaxBytes || hex.EncodeToString(h.Sum(nil)) != file.SHA256) {
				err = errors.New("install_source_changed")
			}
		}
		after, statErr := input.Stat()
		if statErr == nil {
			afterID, e := publicationFile(after)
			if e != nil || afterID != original {
				statErr = errors.New("install_source_changed")
			}
		}
		if err == nil {
			err = statErr
		}
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
		closeOutput()
		input.Close()
		closeSource()
		if err != nil {
			return err
		}
	}
	return p.verifyStable()
}

// Never copy a sentinel byte beyond the declared or cumulative budget. Read it
// only to reject a growing source; a too-long input is a failure, not truncation.
func publicationCopyBounded(output io.Writer, input io.Reader, maximum int64) (int64, error) {
	if maximum < 0 {
		return 0, errors.New("install_payload_limit")
	}
	n, err := io.CopyBuffer(output, io.LimitReader(input, maximum), make([]byte, 64<<10))
	if err != nil {
		return n, err
	}
	var extra [1]byte
	read, err := io.ReadFull(input, extra[:])
	if read != 0 {
		return n, errors.New("install_payload_limit")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, nil
}
