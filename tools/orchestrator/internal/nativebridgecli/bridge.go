package nativebridgecli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

type codeError string

func (e codeError) Error() string { return string(e) }

type LaunchRequest struct {
	Version               int    `json:"version"`
	Interpreter           string `json:"interpreter"`
	InterpreterSHA256     string `json:"interpreter_sha256"`
	PackageRoot           string `json:"package_root"`
	PackageManifest       string `json:"package_manifest"`
	PackageManifestSHA256 string `json:"package_manifest_sha256"`
}

type rebindRequest struct {
	Version               int    `json:"version"`
	OwnerCapability       string `json:"owner_capability"`
	ControlFile           string `json:"control_file"`
	ControllerThread      string `json:"controller_thread"`
	OriginContextID       string `json:"origin_context_id"`
	OriginPID             int    `json:"origin_pid"`
	OriginBirth           string `json:"origin_birth"`
	HostGeneration        string `json:"host_generation"`
	AttachmentProofSHA256 string `json:"attachment_proof_sha256"`
}

type ownerCapability struct {
	Version               int             `json:"version"`
	Path                  string          `json:"path"`
	ControllerThread      string          `json:"controller_thread_id"`
	OriginContextID       string          `json:"origin_context_id"`
	OriginPID             int             `json:"origin_pid"`
	OriginBirth           string          `json:"origin_birth"`
	HostGeneration        string          `json:"host_generation"`
	GenerationNumber      int             `json:"generation_number"`
	AttachmentProofSHA256 string          `json:"attachment_proof_sha256"`
	LeaseID               string          `json:"lease_id"`
	ActivationID          string          `json:"activation_id"`
	OwnerAttachment       json.RawMessage `json:"owner_attachment"`
}

type attachmentPeer struct {
	PID              int    `json:"pid"`
	UID              int    `json:"uid"`
	Birth            string `json:"birth"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

type ownerAttachment struct {
	Version               int            `json:"version"`
	ProfileID             string         `json:"profile_id"`
	ControllerThread      string         `json:"controller_thread_id"`
	ControllerEpoch       int            `json:"controller_epoch"`
	OwnerContextSHA256    string         `json:"owner_context_sha256"`
	LeaseID               string         `json:"lease_id"`
	OwnerConnectionID     string         `json:"owner_connection_id"`
	ActivationID          string         `json:"activation_id"`
	ManifestSHA256        string         `json:"manifest_sha256"`
	HelperGrantSHA256     string         `json:"helper_grant_sha256"`
	ServiceIdentity       attachmentPeer `json:"service_identity"`
	BackendIdentity       attachmentPeer `json:"backend_identity"`
	PrivateSocket         string         `json:"private_socket"`
	PrivateSocketIdentity []uint64       `json:"private_socket_identity"`
	OriginProcess         attachmentPeer `json:"origin_process"`
}

type VerifiedRebind struct {
	OwnerCapability       string
	ControlFile           string
	ControllerThread      string
	OriginContextID       string
	OriginPID             int
	OriginBirth           string
	HostGeneration        string
	AttachmentProofSHA256 string
}

type VerifiedOwner struct {
	OwnerCapability       string
	ControllerThread      string
	OriginContextID       string
	OriginPID             int
	OriginBirth           string
	HostGeneration        string
	AttachmentProofSHA256 string
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	generationPattern = regexp.MustCompile(`^generation-[0-9]{8}$`)
)

func VerifyRebind(requestPath string) (VerifiedRebind, error) {
	var request rebindRequest
	if err := readPrivateJSON(requestPath, &request); err != nil {
		return VerifiedRebind{}, err
	}
	if request.Version != 1 || !absoluteClean(request.OwnerCapability) || !absoluteClean(request.ControlFile) ||
		request.OriginPID <= 0 || request.OriginBirth == "" || !identifierPattern.MatchString(request.OriginContextID) ||
		request.ControllerThread == "" || len(request.ControllerThread) > 128 ||
		!generationPattern.MatchString(request.HostGeneration) || !hexHash(request.AttachmentProofSHA256) {
		return VerifiedRebind{}, codeError("invalid_rebind")
	}
	owner, err := VerifyOwnerCapability(request.OwnerCapability)
	if err != nil {
		return VerifiedRebind{}, err
	}
	if owner.ControllerThread != request.ControllerThread || owner.OriginContextID != request.OriginContextID ||
		owner.OriginPID != request.OriginPID || owner.OriginBirth != request.OriginBirth ||
		owner.HostGeneration != request.HostGeneration || owner.AttachmentProofSHA256 != request.AttachmentProofSHA256 {
		return VerifiedRebind{}, codeError("owner_capability_mismatch")
	}
	controlInfo, err := os.Lstat(request.ControlFile)
	if err != nil || !controlInfo.Mode().IsRegular() || controlInfo.Mode().Perm() != 0o600 {
		return VerifiedRebind{}, codeError("control_file_invalid")
	}
	controlStat, ok := controlInfo.Sys().(*syscall.Stat_t)
	if !ok || controlStat.Nlink != 1 || int(controlStat.Uid) != os.Getuid() {
		return VerifiedRebind{}, codeError("control_file_invalid")
	}
	return VerifiedRebind{
		OwnerCapability: request.OwnerCapability, ControlFile: request.ControlFile,
		ControllerThread: request.ControllerThread, OriginContextID: request.OriginContextID,
		OriginPID: request.OriginPID, OriginBirth: request.OriginBirth,
		HostGeneration: request.HostGeneration, AttachmentProofSHA256: request.AttachmentProofSHA256,
	}, nil
}

func VerifyOwnerCapability(path string) (VerifiedOwner, error) {
	if !absoluteClean(path) {
		return VerifiedOwner{}, codeError("owner_capability_invalid")
	}
	var capability ownerCapability
	if err := readPrivateJSON(path, &capability); err != nil {
		return VerifiedOwner{}, codeError("owner_capability_invalid")
	}
	if capability.Version != 1 || capability.Path != path || capability.OriginPID <= 0 ||
		capability.OriginBirth == "" || capability.ControllerThread == "" || len(capability.ControllerThread) > 128 ||
		!identifierPattern.MatchString(capability.OriginContextID) || !generationPattern.MatchString(capability.HostGeneration) ||
		capability.GenerationNumber <= 0 || capability.LeaseID == "" || capability.ActivationID == "" ||
		!hexHash(capability.AttachmentProofSHA256) {
		return VerifiedOwner{}, codeError("owner_capability_invalid")
	}
	_, err := verifyOwnerAttachment(capability)
	if err != nil {
		return VerifiedOwner{}, err
	}
	birth, err := process.Birth(capability.OriginPID)
	if err != nil || birth != capability.OriginBirth {
		return VerifiedOwner{}, codeError("owner_not_live")
	}
	return VerifiedOwner{
		OwnerCapability: path, ControllerThread: capability.ControllerThread,
		OriginContextID: capability.OriginContextID, OriginPID: capability.OriginPID,
		OriginBirth: capability.OriginBirth, HostGeneration: capability.HostGeneration,
		AttachmentProofSHA256: capability.AttachmentProofSHA256,
	}, nil
}

func verifyOwnerAttachment(capability ownerCapability) (ownerAttachment, error) {
	proof := sha256.Sum256(capability.OwnerAttachment)
	if hex.EncodeToString(proof[:]) != capability.AttachmentProofSHA256 {
		return ownerAttachment{}, codeError("owner_capability_invalid")
	}
	var attachment ownerAttachment
	decoder := json.NewDecoder(bytes.NewReader(capability.OwnerAttachment))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&attachment) != nil || decoder.Decode(new(any)) != io.EOF ||
		attachment.Version != 1 || attachment.ControllerThread != capability.ControllerThread ||
		attachment.LeaseID != capability.LeaseID || attachment.ActivationID != capability.ActivationID ||
		attachment.OriginProcess.PID != capability.OriginPID || attachment.OriginProcess.Birth != capability.OriginBirth ||
		attachment.ControllerEpoch <= 0 || !hexHash(attachment.OwnerContextSHA256) ||
		!hexHash(attachment.ManifestSHA256) || !hexHash(attachment.HelperGrantSHA256) ||
		len(attachment.PrivateSocketIdentity) != 4 || !absoluteClean(attachment.PrivateSocket) {
		return ownerAttachment{}, codeError("owner_capability_invalid")
	}
	if attachment.ServiceIdentity.PID == attachment.OriginProcess.PID ||
		attachment.BackendIdentity.PID == attachment.OriginProcess.PID ||
		attachment.ServiceIdentity.PID == attachment.BackendIdentity.PID {
		return ownerAttachment{}, codeError("owner_capability_invalid")
	}
	for _, peer := range []attachmentPeer{attachment.ServiceIdentity, attachment.BackendIdentity, attachment.OriginProcess} {
		if peer.PID <= 0 || peer.UID != os.Getuid() || peer.Birth == "" || !absoluteClean(peer.Executable) || !hexHash(peer.ExecutableSHA256) {
			return ownerAttachment{}, codeError("owner_capability_invalid")
		}
		birth, err := process.Birth(peer.PID)
		if err != nil || birth != peer.Birth {
			return ownerAttachment{}, codeError("owner_attachment_not_live")
		}
		digest, err := hashRegular(peer.Executable, true)
		if err != nil || digest != peer.ExecutableSHA256 {
			return ownerAttachment{}, codeError("owner_attachment_changed")
		}
	}
	info, err := os.Lstat(attachment.PrivateSocket)
	statValue, ok := infoSys(info)
	identity := attachment.PrivateSocketIdentity
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || !ok ||
		int(statValue.Uid) != os.Getuid() || uint64(statValue.Dev) != identity[0] ||
		uint64(statValue.Ino) != identity[1] || uint64(statValue.Uid) != identity[2] ||
		uint64(statValue.Mode) != identity[3] {
		return ownerAttachment{}, codeError("owner_lease_not_live")
	}
	return attachment, nil
}

type packageManifest struct {
	SchemaVersion int            `json:"schema_version"`
	PackageName   string         `json:"package_name,omitempty"`
	Version       string         `json:"version,omitempty"`
	BinaryPath    string         `json:"binary_path,omitempty"`
	BinarySHA256  string         `json:"binary_sha256,omitempty"`
	Files         []manifestFile `json:"files"`
}

type manifestFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Mode        uint32 `json:"mode"`
	Device      uint64 `json:"device,omitempty"`
	Inode       uint64 `json:"inode,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ModUnixNano int64  `json:"mod_unix_nano,omitempty"`
}

func RunHelper(ctx context.Context, requestPath string, stdin io.Reader, stdout, stderr io.Writer) error {
	var request LaunchRequest
	if err := readPrivateJSON(requestPath, &request); err != nil {
		return err
	}
	script, err := validateLaunch(request)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, request.Interpreter, "-B", script, "helper")
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C", "PYTHONDONTWRITEBYTECODE=1"}
	if temporary := os.TempDir(); temporary != "" {
		command.Env = append(command.Env, "TMPDIR="+temporary)
	}
	if err = command.Run(); err != nil {
		return codeError("native_bridge_helper_failed")
	}
	return nil
}

func validateLaunch(request LaunchRequest) (string, error) {
	if request.Version != 1 || !absoluteClean(request.Interpreter) || !absoluteClean(request.PackageRoot) ||
		!absoluteClean(request.PackageManifest) || request.PackageManifest != filepath.Join(request.PackageRoot, "manifest.json") ||
		!hexHash(request.InterpreterSHA256) || !hexHash(request.PackageManifestSHA256) {
		return "", codeError("invalid_native_bridge_launch")
	}
	if err := validatePackageRoot(request.PackageRoot); err != nil {
		return "", err
	}
	if digest, err := hashRegular(request.Interpreter, true); err != nil || digest != request.InterpreterSHA256 {
		return "", codeError("interpreter_changed")
	}
	if digest, err := hashRegular(request.PackageManifest, false); err != nil || digest != request.PackageManifestSHA256 {
		return "", codeError("package_manifest_changed")
	}
	var manifest packageManifest
	if err := readJSON(request.PackageManifest, &manifest, 16<<20); err != nil || manifest.SchemaVersion != 1 {
		return "", codeError("package_manifest_invalid")
	}
	const scriptName = "scripts/native-product-bridge.py"
	var entry *manifestFile
	for index := range manifest.Files {
		if manifest.Files[index].Path == scriptName {
			if entry != nil {
				return "", codeError("package_manifest_invalid")
			}
			entry = &manifest.Files[index]
		}
	}
	if entry == nil || entry.Mode != 0o700 || !hexHash(entry.SHA256) {
		return "", codeError("native_bridge_script_missing")
	}
	script := filepath.Join(request.PackageRoot, filepath.FromSlash(scriptName))
	if !absoluteClean(script) {
		return "", codeError("native_bridge_script_changed")
	}
	info, err := os.Lstat(script)
	statValue, statOK := infoSys(info)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(entry.Mode) || !statOK ||
		statValue.Nlink != 1 || int(statValue.Uid) != os.Getuid() {
		return "", codeError("native_bridge_script_changed")
	}
	if digest, hashErr := hashRegular(script, false); hashErr != nil || digest != entry.SHA256 {
		return "", codeError("native_bridge_script_changed")
	}
	return script, nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	value, ok := info.Sys().(*syscall.Stat_t)
	return value, ok
}

func validatePackageRoot(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return codeError("package_root_changed")
	}
	info, err := os.Lstat(path)
	statValue, ok := infoSys(info)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		!ok || int(statValue.Uid) != os.Getuid() {
		return codeError("package_root_changed")
	}
	return nil
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func hexHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashRegular(path string, executable bool) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 {
		return "", codeError("invalid_file")
	}
	statValue, ok := info.Sys().(*syscall.Stat_t)
	if !ok || statValue.Nlink != 1 || (statValue.Uid != 0 && int(statValue.Uid) != os.Getuid()) || (executable && info.Mode().Perm()&0o111 == 0) {
		return "", codeError("invalid_file")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readPrivateJSON(path string, target any) error {
	if !absoluteClean(path) {
		return codeError("invalid_request_path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return codeError("invalid_request_file")
	}
	statValue, ok := info.Sys().(*syscall.Stat_t)
	if !ok || statValue.Nlink != 1 || int(statValue.Uid) != os.Getuid() {
		return codeError("invalid_request_file")
	}
	return readJSON(path, target, 64<<10)
}

func readJSON(path string, target any, maximum int64) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return codeError("invalid_json_file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximum+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return codeError("invalid_json_file")
	}
	return nil
}
