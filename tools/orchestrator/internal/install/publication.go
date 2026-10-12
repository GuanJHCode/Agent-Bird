//go:build !windows

package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var ErrInstallPublicationPending = errors.New("install_publication_pending")
var ErrInstallPublicationConflict = errors.New("install_publication_conflict")
var ErrInstallOriginalInputsRequired = errors.New("original_install_inputs_required")

type installIntent struct {
	SchemaVersion    int                          `json:"schema_version"`
	Request          installRequest               `json:"request"`
	RequestSHA256    string                       `json:"request_sha256"`
	Stage            string                       `json:"stage"`
	Directory        publicationDirectoryIdentity `json:"version_directory_identity"`
	Manifest         []byte                       `json:"manifest"`
	ManifestSHA256   string                       `json:"manifest_sha256"`
	ManifestIdentity publicationFileIdentity      `json:"manifest_file_identity"`
	PayloadSHA256    string                       `json:"payload_sha256"`
	BinarySHA256     string                       `json:"binary_sha256"`
	CreatedAt        string                       `json:"created_at"`
}

func publicationBytesDigest(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}
func publicationCheckpoint(checkpoint func(string) error, point string) error {
	if checkpoint != nil {
		return checkpoint(point)
	}
	return nil
}
func publicationConflict(err error) error {
	if err == nil {
		return ErrInstallPublicationConflict
	}
	return errors.Join(ErrInstallPublicationConflict, err)
}
func publicationPending(err error) error { return errors.Join(ErrInstallPublicationPending, err) }

func validPublicationDirectory(id publicationDirectoryIdentity) bool {
	return id.Inode != 0 && int(id.UID) == os.Geteuid() && id.Mode == 0700
}
func validPublicationManifest(id publicationFileIdentity) bool {
	return id.Directory.Inode != 0 && int(id.Directory.UID) == os.Geteuid() && id.Directory.Mode == 0600 && id.Links == 1 && id.Size > 0 && id.Size <= 4<<20
}

func decodeInstallIntent(body []byte, destination, version string) (installIntent, error) {
	var i installIntent
	if !utf8.Valid(body) || strictJSONBounded(body, &i, false, installMaxRecord) != nil {
		return i, ErrInstallPublicationConflict
	}
	expected, err := installExpectedPayload(i.Request)
	directories, directoryErr := installExpectedDirectories(i.Request)
	payloadDigest := publicationDigest(struct {
		Files       []installPayloadFile
		Directories []installPayloadDirectory
	}{expected, directories})
	if err != nil || directoryErr != nil || i.SchemaVersion != 1 || i.Request.DestinationRoot != destination || i.Request.Version != version || i.RequestSHA256 != publicationDigest(i.Request) || i.PayloadSHA256 != payloadDigest || i.BinarySHA256 != i.Request.Binary.SHA256 || !validPublicationDirectory(i.Directory) || !validPublicationManifest(i.ManifestIdentity) || int64(len(i.Manifest)) != i.ManifestIdentity.Size || i.ManifestSHA256 != publicationBytesDigest(i.Manifest) || !regexp.MustCompile(`^\.stage-`+regexp.QuoteMeta(version)+`-[0-9a-f]{12}$`).MatchString(i.Stage) {
		return i, ErrInstallPublicationConflict
	}
	created, err := time.Parse(time.RFC3339Nano, i.CreatedAt)
	if err != nil || created.IsZero() {
		return i, ErrInstallPublicationConflict
	}
	var m manifest
	if strictJSONBounded(i.Manifest, &m, false, 4<<20) != nil || validatePublicationManifest(m, i, expected) != nil {
		return i, ErrInstallPublicationConflict
	}
	return i, nil
}

func validatePublicationManifest(m manifest, i installIntent, expected []installPayloadFile) error {
	if m.SchemaVersion != 1 || m.PackageName != "codex-orchestrator" || m.Version != i.Request.Version || m.BinaryPath != "bin/codex-orchestrator" || m.BinarySHA256 != i.BinarySHA256 || len(m.Files) != len(expected) {
		return ErrInstallPublicationConflict
	}
	for n, file := range m.Files {
		want := expected[n]
		if file.Path != want.Path || file.Mode != want.Mode || file.Size != want.Size || file.SHA256 != want.SHA256 || file.Inode == 0 {
			return ErrInstallPublicationConflict
		}
	}
	directories, err := installExpectedDirectories(i.Request)
	if err != nil || len(m.Directories) != len(directories) {
		return ErrInstallPublicationConflict
	}
	for n, directory := range m.Directories {
		if directory.Path != directories[n].Path || !validPublicationDirectory(directory.Identity) {
			return ErrInstallPublicationConflict
		}
	}
	return nil
}

func publicationValidateManifestTree(root *os.File, value manifest) error {
	if value.Directories == nil || len(value.Directories) > installMaxFiles || len(value.Files) > installMaxFiles {
		return ErrInstallPublicationConflict
	}
	tree, err := publicationScan(root)
	if err != nil || len(tree.Directories) != len(value.Directories) || len(tree.Files) != len(value.Files)+1 {
		return ErrInstallPublicationConflict
	}
	for n, directory := range value.Directories {
		if !publicationRelative(directory.Path) || !validPublicationDirectory(directory.Identity) || tree.Directories[n].Path != directory.Path || tree.DirectoryIDs[directory.Path] != directory.Identity {
			return ErrInstallPublicationConflict
		}
	}
	observed := map[string]installPayloadFile{}
	for _, f := range tree.Files {
		observed[f.Path] = f
	}
	previous := ""
	for _, f := range value.Files {
		actual, ok := observed[f.Path]
		identity := tree.FileIDs[f.Path]
		if !publicationRelative(f.Path) || f.Path <= previous || f.Path == "manifest.json" || !ok || actual.Mode != f.Mode || actual.Size != f.Size || actual.SHA256 != f.SHA256 || identity.Directory.Device != f.Device || identity.Directory.Inode != f.Inode || identity.Modified != f.ModUnixNano {
			return ErrInstallPublicationConflict
		}
		previous = f.Path
	}
	return nil
}

func (a *publicationAnchors) seal(stage *os.File, name string, id publicationDirectoryIdentity, p *preparedInstallRequest, checkpoint func(string) error) (installIntent, []byte, error) {
	if err := p.copyTo(stage); err != nil {
		return installIntent{}, nil, err
	}
	tree, err := publicationScan(stage)
	if err != nil {
		return installIntent{}, nil, err
	}
	expected, _ := installExpectedPayload(p.request)
	directories, _ := installExpectedDirectories(p.request)
	if !reflect.DeepEqual(tree.Files, expected) || !reflect.DeepEqual(tree.Directories, directories) {
		return installIntent{}, nil, errors.New("install_copied_payload_mismatch")
	}
	root := filepath.Join(a.rootPath, "versions", name)
	if _, err = validatePayload(root, false); err != nil {
		return installIntent{}, nil, err
	}
	if _, err = ValidateRuntimeBundle(root); err != nil {
		return installIntent{}, nil, err
	}
	value := manifest{SchemaVersion: 1, PackageName: "codex-orchestrator", Version: p.request.Version, BinaryPath: "bin/codex-orchestrator", BinarySHA256: p.request.Binary.SHA256}
	for _, f := range tree.Files {
		identity := tree.FileIDs[f.Path]
		value.Files = append(value.Files, manifestFile{Path: f.Path, SHA256: f.SHA256, Mode: f.Mode, Device: identity.Directory.Device, Inode: identity.Directory.Inode, Size: f.Size, ModUnixNano: identity.Modified})
	}
	for _, directory := range tree.Directories {
		value.Directories = append(value.Directories, manifestDirectory{directory.Path, tree.DirectoryIDs[directory.Path]})
	}
	manifestBody, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return installIntent{}, nil, err
	}
	manifestBody = append(manifestBody, '\n')
	if len(manifestBody) > 4<<20 {
		return installIntent{}, nil, errors.New("install_payload_limit")
	}
	manifestID, err := publicationWriteAt(stage, "manifest.json", manifestBody)
	if err != nil {
		return installIntent{}, nil, err
	}
	if err = publicationSyncTree(stage); err != nil {
		return installIntent{}, nil, err
	}
	if err = a.versions.Sync(); err != nil {
		return installIntent{}, nil, err
	}
	if err = a.validate(); err != nil {
		return installIntent{}, nil, err
	}
	i := installIntent{SchemaVersion: 1, Request: p.request, RequestSHA256: p.digest, Stage: name, Directory: id, Manifest: manifestBody, ManifestSHA256: publicationBytesDigest(manifestBody), ManifestIdentity: manifestID, PayloadSHA256: p.payloadDigest, BinarySHA256: p.request.Binary.SHA256, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	body, err := json.Marshal(i)
	if err != nil || len(body)+1 > installMaxRecord {
		return installIntent{}, nil, errors.New("install_payload_limit")
	}
	body = append(body, '\n')
	if err = publicationCheckpoint(checkpoint, "stage-sealed"); err != nil {
		return installIntent{}, nil, err
	}
	return i, body, nil
}

func (a *publicationAnchors) validateSealed(i installIntent, name string) (RuntimeIdentity, error) {
	if err := a.validate(); err != nil {
		return RuntimeIdentity{}, err
	}
	root, err := publicationOpen(a.versions, name, true)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	defer root.Close()
	info, err := root.Stat()
	if err != nil {
		return RuntimeIdentity{}, err
	}
	id, err := publicationDirectory(info)
	if err != nil || id != i.Directory {
		return RuntimeIdentity{}, ErrInstallPublicationConflict
	}
	body, manifestID, err := publicationReadAt(root, "manifest.json", 4<<20)
	if err != nil || manifestID != i.ManifestIdentity || !bytes.Equal(body, i.Manifest) {
		return RuntimeIdentity{}, ErrInstallPublicationConflict
	}
	var value manifest
	if strictJSONBounded(i.Manifest, &value, false, 4<<20) != nil || publicationValidateManifestTree(root, value) != nil {
		return RuntimeIdentity{}, ErrInstallPublicationConflict
	}
	path := filepath.Join(a.rootPath, "versions", name)
	if _, err = validatePayload(path, true); err != nil {
		return RuntimeIdentity{}, err
	}
	runtime, err := ValidateRuntimeBundle(path)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	if err = publicationSamePath(root, path); err != nil {
		return RuntimeIdentity{}, err
	}
	if err = a.validate(); err != nil {
		return RuntimeIdentity{}, err
	}
	return runtime, nil
}

func receiptForInstall(i installIntent, intentSHA string) manifestReceipt {
	return manifestReceipt{Version: i.Request.Version, ManifestSHA256: i.ManifestSHA256, RequestSHA256: i.RequestSHA256, InstallIntentSHA256: intentSHA, VersionDirectoryIdentity: &i.Directory, ManifestFileIdentity: &i.ManifestIdentity}
}

func decodeManifestReceipt(body []byte, version string) (manifestReceipt, error) {
	var r manifestReceipt
	if len(body) > 4096 || !utf8.Valid(body) {
		return r, errors.New("installed_receipt_invalid")
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil {
		return r, errors.New("installed_receipt_invalid")
	}
	// encoding/json accepts case-folded struct field aliases. The receipt's
	// closed wire names must agree with the provenance presence validator.
	for name := range members {
		switch name {
		case "version", "manifest_sha256", "request_sha256", "install_intent_sha256", "version_directory_identity", "manifest_file_identity":
		default:
			return r, errors.New("installed_receipt_invalid")
		}
	}
	if strictJSON(body, &r) != nil || r.Version != version || !versionPattern.MatchString(r.Version) || !validHexDigest(r.ManifestSHA256) {
		return r, errors.New("installed_receipt_invalid")
	}
	provenance := false
	for _, name := range []string{"request_sha256", "install_intent_sha256", "version_directory_identity", "manifest_file_identity"} {
		_, present := members[name]
		provenance = provenance || present
	}
	if provenance && (!validHexDigest(r.RequestSHA256) || !validHexDigest(r.InstallIntentSHA256) || r.VersionDirectoryIdentity == nil || r.ManifestFileIdentity == nil || !validPublicationDirectory(*r.VersionDirectoryIdentity) || !validPublicationManifest(*r.ManifestFileIdentity)) {
		return r, errors.New("installed_receipt_invalid")
	}
	return r, nil
}

func publicationCleanupName(version, hash string) string {
	return ".cleanup-" + version + "-" + hash + ".json"
}

func publicationRecordName(name string) (version, cleanupHash string, valid bool) {
	if !strings.HasSuffix(name, ".json") {
		return "", "", false
	}
	base := strings.TrimSuffix(name, ".json")
	if strings.HasPrefix(base, ".cleanup-") {
		base = strings.TrimPrefix(base, ".cleanup-")
		if len(base) < 66 || base[len(base)-65] != '-' {
			return "", "", false
		}
		cleanupHash = base[len(base)-64:]
		if !validHexDigest(cleanupHash) {
			return "", "", false
		}
		base = base[:len(base)-65]
	}
	if len(base) > 128 || !versionPattern.MatchString(base) {
		return "", "", false
	}
	return base, cleanupHash, true
}

// Only uninstall uses this bounded name inventory to block. Unknown cleanup
// names are never recovery candidates and never removed by this inspection.
func checkUninstallPublicationRecords(destination, version string) error {
	name := filepath.Join(destination, "install-intents")
	if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return publicationConflict(err)
	}
	parent, err := publicationOpenRoot(name)
	if err != nil {
		return publicationConflict(err)
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		return ErrInstallPublicationConflict
	}
	entries, err := parent.ReadDir(installMaxFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > installMaxFiles {
		return publicationConflict(err)
	}
	pending := false
	for _, entry := range entries {
		target, _, valid := publicationRecordName(entry.Name())
		if entry.IsDir() || !valid {
			return ErrInstallPublicationConflict
		}
		pending = pending || target == version
	}
	if err = publicationSamePath(parent, name); err != nil {
		return publicationConflict(err)
	}
	if pending {
		return ErrInstallPublicationPending
	}
	return nil
}

func checkInstallPublicationPending(destination, version string) (result error) {
	parentPath := filepath.Join(destination, "install-intents")
	var parent *os.File
	if _, err := os.Lstat(parentPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return publicationConflict(err)
		}
	} else {
		var err error
		parent, err = publicationOpenRoot(parentPath)
		if err != nil {
			return publicationConflict(err)
		}
		info, err := parent.Stat()
		if err != nil {
			parent.Close()
			return publicationConflict(err)
		}
		identity, err := publicationDirectory(info)
		if err != nil || identity.Mode != 0700 {
			parent.Close()
			return ErrInstallPublicationConflict
		}
		defer func() {
			// Classify only the same original private parent at return; never
			// adopt moved or replacement evidence even on a pending refusal.
			info, err := parent.Stat()
			if err != nil {
				result = publicationConflict(err)
			} else if current, err := publicationDirectory(info); err != nil || current != identity || publicationSamePath(parent, parentPath) != nil {
				result = ErrInstallPublicationConflict
			}
			parent.Close()
		}()
		if exists, err := publicationExists(parent, version+".json"); err != nil {
			return publicationConflict(err)
		} else if exists {
			return ErrInstallPublicationPending
		}
	}
	body, err := readOwnedRegular(filepath.Join(destination, "manifests", version+".json"), 0600, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return publicationConflict(err)
	}
	r, err := decodeManifestReceipt(body, version)
	if err != nil {
		return publicationConflict(err)
	}
	if r.InstallIntentSHA256 != "" {
		// New publication always creates this directory. Only legacy receipts
		// with fully omitted provenance can lack it after a valid commit.
		if parent == nil {
			return ErrInstallPublicationConflict
		}
		if exists, err := publicationExists(parent, publicationCleanupName(version, r.InstallIntentSHA256)); err != nil {
			return publicationConflict(err)
		} else if exists {
			return ErrInstallPublicationPending
		}
	}
	return nil
}

// Doctor inspects only retained publication records and the tree they name. It
// neither creates a lock nor adopts any temporary directory or unknown cleanup.
func doctorInstallPublications(destination string) ([]string, error) {
	parent := filepath.Join(destination, "install-intents")
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	a, err := openPublicationAnchors(destination)
	if err != nil {
		return []string{"install_intents_unsafe"}, nil
	}
	defer a.close()
	listing, err := publicationOpen(a.intents, ".", true)
	if err != nil {
		return nil, err
	}
	defer listing.Close()
	entries, err := listing.ReadDir(installMaxFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > installMaxFiles {
		return []string{"install_intents_limit"}, nil
	}
	var issues []string
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		version, cleanupHash, valid := publicationRecordName(name)
		if entry.IsDir() || !valid {
			issues = append(issues, "install_intent_unknown:"+name)
			continue
		}
		if seen[version] {
			continue
		}
		seen[version] = true
		var receipt *manifestReceipt
		if exists, e := publicationExists(a.manifests, version+".json"); e != nil {
			issues = append(issues, "install_conflict:"+version)
			continue
		} else if exists {
			body, _, e := publicationReadAt(a.manifests, version+".json", 4096)
			r, parseErr := decodeManifestReceipt(body, version)
			if e != nil || parseErr != nil {
				issues = append(issues, "install_conflict:"+version)
				continue
			}
			receipt = &r
		}
		// An unknown cleanup name is evidence, never an input to recovery.
		if cleanupHash != "" && (receipt == nil || receipt.InstallIntentSHA256 != cleanupHash) {
			issues = append(issues, "install_intent_unknown:"+name)
			continue
		}
		i, body, _, _, exists, e := a.readIntent(version, receipt)
		if e != nil || !exists {
			issues = append(issues, "install_conflict:"+version)
			continue
		}
		stageExists, e1 := publicationExists(a.versions, i.Stage)
		versionExists, e2 := publicationExists(a.versions, version)
		if e1 != nil || e2 != nil || stageExists == versionExists || receipt != nil && (stageExists || !reflect.DeepEqual(*receipt, receiptForInstall(i, publicationBytesDigest(body)))) {
			issues = append(issues, "install_conflict:"+version)
			continue
		}
		active := version
		state := "version"
		if stageExists {
			active, state = i.Stage, "stage"
		}
		if _, e = a.validateSealed(i, active); e != nil {
			issues = append(issues, "install_conflict:"+version)
			continue
		}
		if receipt != nil {
			state = "cleanup"
		}
		issues = append(issues, "install_pending:"+version+":"+state)
		if _, e = os.Lstat(i.Request.SourceRoot); errors.Is(e, os.ErrNotExist) {
			issues = append(issues, "original_install_inputs_required:"+version)
		} else if _, e = os.Lstat(i.Request.BinaryPath); errors.Is(e, os.ErrNotExist) {
			issues = append(issues, "original_install_inputs_required:"+version)
		}
	}
	return unique(issues), nil
}

func (a *publicationAnchors) readIntent(version string, receipt *manifestReceipt) (installIntent, []byte, string, publicationFileIdentity, bool, error) {
	normal := version + ".json"
	normalExists, err := publicationExists(a.intents, normal)
	if err != nil {
		return installIntent{}, nil, "", publicationFileIdentity{}, false, err
	}
	cleanup := ""
	cleanupExists := false
	if receipt != nil && receipt.InstallIntentSHA256 != "" {
		cleanup = publicationCleanupName(version, receipt.InstallIntentSHA256)
		cleanupExists, err = publicationExists(a.intents, cleanup)
		if err != nil {
			return installIntent{}, nil, "", publicationFileIdentity{}, false, err
		}
	}
	if normalExists && cleanupExists {
		return installIntent{}, nil, "", publicationFileIdentity{}, false, ErrInstallPublicationConflict
	}
	if !normalExists && !cleanupExists {
		return installIntent{}, nil, "", publicationFileIdentity{}, false, nil
	}
	name := normal
	if cleanupExists {
		name = cleanup
	}
	body, id, err := publicationReadAt(a.intents, name, installMaxRecord)
	if err != nil {
		return installIntent{}, nil, name, id, true, publicationConflict(err)
	}
	i, err := decodeInstallIntent(body, a.rootPath, version)
	if err != nil {
		return i, body, name, id, true, err
	}
	if cleanupExists && (receipt == nil || publicationBytesDigest(body) != receipt.InstallIntentSHA256) {
		return i, body, name, id, true, ErrInstallPublicationConflict
	}
	return i, body, name, id, true, nil
}

// Support is deliberately the private 0700 namespace and one cooperative lock.
// Rename plus identity verification is not a conditional unlink against a
// same-UID external writer that replaces a name after the final verification.
func (a *publicationAnchors) cleanupIntent(i installIntent, body []byte, name string, identity publicationFileIdentity, checkpoint func(string) error) error {
	hash := publicationBytesDigest(body)
	cleanup := publicationCleanupName(i.Request.Version, hash)
	if err := publicationCheckpoint(checkpoint, "before-intent-cleanup"); err != nil {
		return publicationPending(err)
	}
	if err := a.validate(); err != nil {
		return publicationPending(err)
	}
	current, id, err := publicationReadAt(a.intents, name, installMaxRecord)
	if err != nil || id != identity || !bytes.Equal(current, body) {
		return publicationConflict(err)
	}
	if name != cleanup {
		if err = publicationRenameNoReplace(int(a.intents.Fd()), name, int(a.intents.Fd()), cleanup); err != nil {
			return publicationPending(err)
		}
	}
	current, id, err = publicationReadAt(a.intents, cleanup, installMaxRecord)
	if err != nil || id != identity || !bytes.Equal(current, body) {
		return publicationConflict(err)
	}
	if err = a.intents.Sync(); err != nil {
		return publicationPending(err)
	}
	if err = publicationCheckpoint(checkpoint, "intent-cleanup-renamed"); err != nil {
		return publicationPending(err)
	}
	if err = a.validate(); err != nil {
		return publicationPending(err)
	}
	current, id, err = publicationReadAt(a.intents, cleanup, installMaxRecord)
	if err != nil || id != identity || !bytes.Equal(current, body) {
		return publicationConflict(err)
	}
	if err = unix.Unlinkat(int(a.intents.Fd()), cleanup, 0); err != nil {
		return publicationPending(err)
	}
	if err = a.intents.Sync(); err != nil {
		return publicationPending(err)
	}
	return publicationCheckpoint(checkpoint, "intent-cleaned")
}

func installationResult(root string, value manifest, runtime RuntimeIdentity, hash string) InstallResult {
	paths := make([]string, 0, len(value.Files))
	for _, f := range value.Files {
		paths = append(paths, f.Path)
	}
	runtime.Root = filepath.Join(root, "runtime", "g0")
	runtime.ManifestPath = filepath.Join(runtime.Root, "runtime-manifest.json")
	return InstallResult{Version: value.Version, VersionRoot: root, ManifestPath: filepath.Join(root, "manifest.json"), ManifestSHA256: hash, BinarySHA256: value.BinarySHA256, Files: paths, Runtime: runtime}
}

func (a *publicationAnchors) replayCommitted(p *preparedInstallRequest, r manifestReceipt, checkpoint func(string) error) (InstallResult, error) {
	if r.RequestSHA256 == "" {
		return InstallResult{}, ErrVersionExists
	}
	if r.RequestSHA256 != p.digest {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	i, body, name, id, hasIntent, err := a.readIntent(p.request.Version, &r)
	if err != nil {
		return InstallResult{}, err
	}
	if hasIntent {
		if i.RequestSHA256 != p.digest || !reflect.DeepEqual(r, receiptForInstall(i, publicationBytesDigest(body))) {
			return InstallResult{}, ErrInstallPublicationConflict
		}
		if exists, err := publicationExists(a.versions, i.Stage); err != nil || exists {
			return InstallResult{}, ErrInstallPublicationConflict
		}
		if _, err = a.validateSealed(i, p.request.Version); err != nil {
			return InstallResult{}, publicationConflict(err)
		}
	}
	if err = verifyInstalledVersionContent(a.rootPath, p.request.Version); err != nil {
		return InstallResult{}, publicationConflict(err)
	}
	root := filepath.Join(a.rootPath, "versions", p.request.Version)
	rootFD, err := publicationOpen(a.versions, p.request.Version, true)
	if err != nil {
		return InstallResult{}, err
	}
	defer rootFD.Close()
	tree, err := publicationScan(rootFD)
	if err != nil {
		return InstallResult{}, publicationConflict(err)
	}
	var value manifest
	manifestBody, _, err := publicationReadAt(rootFD, "manifest.json", 4<<20)
	if err != nil || strictJSONBounded(manifestBody, &value, false, 4<<20) != nil {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	expected, _ := installExpectedPayload(p.request)
	var payload []installPayloadFile
	for _, file := range tree.Files {
		if file.Path != "manifest.json" {
			payload = append(payload, file)
		}
	}
	directories, _ := installExpectedDirectories(p.request)
	if !reflect.DeepEqual(payload, expected) || !reflect.DeepEqual(tree.Directories, directories) {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	runtime, err := ValidateRuntimeBundle(root)
	if err != nil {
		return InstallResult{}, publicationConflict(err)
	}
	if err = p.verifyStable(); err != nil {
		return InstallResult{}, err
	}
	for _, dir := range []*os.File{a.versions, a.manifests, a.intents} {
		if err = dir.Sync(); err != nil {
			return InstallResult{}, publicationPending(err)
		}
	}
	if hasIntent {
		if err = a.cleanupIntent(i, body, name, id, checkpoint); err != nil {
			return InstallResult{}, err
		}
	}
	if err = a.validate(); err != nil {
		return InstallResult{}, err
	}
	return installationResult(root, value, runtime, r.ManifestSHA256), nil
}

func installPublication(options InstallOptions, p *preparedInstallRequest, checkpoint func(string) error) (InstallResult, error) {
	for _, name := range []string{"versions", "pins", "manifests", "uninstall-intents", "install-intents"} {
		if err := os.MkdirAll(filepath.Join(options.DestinationRoot, name), 0700); err != nil {
			return InstallResult{}, err
		}
	}
	lock, err := acquireInstallLock(options.DestinationRoot)
	if err != nil {
		return InstallResult{}, err
	}
	defer releaseInstallLock(lock)
	a, err := openPublicationAnchors(options.DestinationRoot)
	if err != nil {
		return InstallResult{}, err
	}
	defer a.close()
	if err = verifyPrivateDirectory(filepath.Join(options.DestinationRoot, "pins")); err != nil {
		return InstallResult{}, err
	}
	if err = verifyPrivateDirectory(filepath.Join(options.DestinationRoot, "uninstall-intents")); err != nil {
		return InstallResult{}, err
	}
	if _, exists, err := readUninstallIntent(filepath.Join(options.DestinationRoot, "uninstall-intents", options.Version+".json"), options.Version); err != nil || exists {
		return InstallResult{}, publicationConflict(err)
	}
	receiptExists, err := publicationExists(a.manifests, options.Version+".json")
	if err != nil {
		return InstallResult{}, err
	}
	var receipt *manifestReceipt
	if receiptExists {
		body, _, err := publicationReadAt(a.manifests, options.Version+".json", 4096)
		if err != nil {
			return InstallResult{}, publicationConflict(err)
		}
		r, err := decodeManifestReceipt(body, options.Version)
		if err != nil {
			return InstallResult{}, publicationConflict(err)
		}
		receipt = &r
	}
	i, body, intentName, _, hasIntent, err := a.readIntent(options.Version, receipt)
	if err != nil {
		return InstallResult{}, err
	}
	versionExists, err := publicationExists(a.versions, options.Version)
	if err != nil {
		return InstallResult{}, err
	}
	if receipt != nil {
		if !versionExists {
			return InstallResult{}, ErrInstallPublicationConflict
		}
		return a.replayCommitted(p, *receipt, checkpoint)
	}
	if !hasIntent {
		if versionExists {
			return InstallResult{}, ErrVersionExists
		}
		stage, name, id, err := a.makeStage(options.Version)
		if err != nil {
			return InstallResult{}, err
		}
		i, body, err = a.seal(stage, name, id, p, checkpoint)
		stage.Close()
		if err != nil {
			return InstallResult{}, err
		}
		intentName = options.Version + ".json"
		if err = a.publishRecord(a.intents, intentName, body); err != nil {
			return InstallResult{}, err
		}
		if err = a.intents.Sync(); err != nil {
			return InstallResult{}, publicationPending(err)
		}
		_, _, err = publicationReadAt(a.intents, intentName, installMaxRecord)
		if err != nil {
			return InstallResult{}, err
		}
		if err = publicationCheckpoint(checkpoint, "intent-published"); err != nil {
			return InstallResult{}, err
		}
	}
	if i.RequestSHA256 != p.digest || i.PayloadSHA256 != p.payloadDigest || i.BinarySHA256 != p.request.Binary.SHA256 {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	stageExists, err := publicationExists(a.versions, i.Stage)
	if err != nil {
		return InstallResult{}, err
	}
	if stageExists == versionExists {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	if err = a.intents.Sync(); err != nil {
		return InstallResult{}, publicationPending(err)
	}
	if stageExists {
		if _, err = a.validateSealed(i, i.Stage); err != nil {
			return InstallResult{}, publicationConflict(err)
		}
		if err = p.verifyStable(); err != nil {
			return InstallResult{}, err
		}
		if err = a.publishStage(i.Stage, options.Version, i.Directory); err != nil {
			return InstallResult{}, err
		}
		if err = publicationCheckpoint(checkpoint, "version-published"); err != nil {
			return InstallResult{}, err
		}
	}
	if err = a.versions.Sync(); err != nil {
		return InstallResult{}, publicationPending(err)
	}
	if err = publicationCheckpoint(checkpoint, "version-parent-synced"); err != nil {
		return InstallResult{}, err
	}
	if _, err = a.validateSealed(i, options.Version); err != nil {
		return InstallResult{}, publicationConflict(err)
	}
	if err = p.verifyStable(); err != nil {
		return InstallResult{}, err
	}
	r := receiptForInstall(i, publicationBytesDigest(body))
	receiptBody, err := json.Marshal(r)
	if err != nil || len(receiptBody)+1 > 4096 {
		return InstallResult{}, ErrInstallPublicationConflict
	}
	receiptBody = append(receiptBody, '\n')
	if err = a.publishRecord(a.manifests, options.Version+".json", receiptBody); err != nil {
		return InstallResult{}, err
	}
	if err = publicationCheckpoint(checkpoint, "index-published"); err != nil {
		return InstallResult{}, err
	}
	for _, dir := range []*os.File{a.manifests, a.versions} {
		if err = dir.Sync(); err != nil {
			return InstallResult{}, publicationPending(err)
		}
	}
	if err = publicationCheckpoint(checkpoint, "index-parent-synced"); err != nil {
		return InstallResult{}, err
	}
	return a.replayCommitted(p, r, checkpoint)
}
