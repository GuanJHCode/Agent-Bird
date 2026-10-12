package coordinator

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The two independent keys have separate purposes. The owner receives only
// the signing public key; neither private key is an owner/worker credential.
type originalOwnerKeys struct {
	sign ed25519.PrivateKey
	wrap []byte
	id   string
}
type originalOwnerKeyFile struct {
	Version     int    `json:"version"`
	SignSeed    string `json:"sign_seed"`
	WrappingKey string `json:"wrapping_key"`
}

func ownerPrivateDirectory(state string) (string, error) {
	dir := filepath.Join(state, "owner-runtime")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", store.CodeError("owner_runtime_directory_unsafe")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return "", store.CodeError("owner_runtime_directory_unsafe")
	}
	return dir, nil
}

func readOwnerPrivateFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() < 1 || info.Size() > limit {
		return nil, store.CodeError("owner_runtime_file_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) || !os.SameFile(info, after) || info.Size() != int64(len(body)) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, store.CodeError("owner_runtime_file_changed")
	}
	return body, nil
}

// Publication uses one atomic, exclusive rename: there is no two-link crash
// window. Keep the written descriptor open and authenticate both names against
// that original inode before trusting the published identity.
type ownerPublicationHooks struct {
	before       func(string) error
	beforeRename func(string) error
	after        func() error
}

func createOwnerPrivateFile(path string, value any) error {
	return createOwnerPrivateFileWithHooks(path, value, ownerPublicationHooks{})
}
func createOwnerPrivateFileWithHooks(path string, value any, hooks ownerPublicationHooks) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > 4096 {
		return store.CodeError("owner_runtime_file_too_large")
	}
	parent := filepath.Dir(path)
	dir, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	anchor, err := dir.Stat()
	if err != nil {
		return err
	}
	st, ok := anchor.Sys().(*syscall.Stat_t)
	if !ok || !anchor.IsDir() || anchor.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return store.CodeError("owner_runtime_directory_unsafe")
	}
	stage, err := os.CreateTemp(parent, ".owner-runtime-*.new")
	if err != nil {
		return err
	}
	defer stage.Close()
	name := stage.Name()
	original, err := stage.Stat()
	if err != nil {
		return err
	}
	defer func() {
		if named, e := os.Lstat(name); e == nil && os.SameFile(original, named) {
			_ = os.Remove(name)
		}
	}()
	if _, err = stage.Write(body); err != nil {
		return err
	}
	if err = stage.Sync(); err != nil {
		return err
	}
	if hooks.before != nil {
		if err = hooks.before(name); err != nil {
			return err
		}
	}
	verify := func(namedPath string) error {
		currentParent, e := os.Lstat(parent)
		if e != nil || !os.SameFile(anchor, currentParent) {
			return store.CodeError("owner_runtime_directory_changed")
		}
		current, e := stage.Stat()
		if e != nil {
			return e
		}
		named, e := os.Lstat(namedPath)
		stat, valid := current.Sys().(*syscall.Stat_t)
		if e != nil || !os.SameFile(original, current) || !os.SameFile(current, named) || !valid || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || current.Size() != int64(len(body)) {
			return store.CodeError("owner_runtime_file_changed")
		}
		actual, e := io.ReadAll(io.NewSectionReader(stage, 0, int64(len(body)+1)))
		if e != nil || !bytes.Equal(body, actual) {
			return store.CodeError("owner_runtime_file_changed")
		}
		return nil
	}
	if err = verify(name); err != nil {
		return err
	}
	if hooks.beforeRename != nil {
		if err = hooks.beforeRename(name); err != nil {
			return err
		}
	}
	if err = ownerRenameNoReplace(int(dir.Fd()), filepath.Base(name), filepath.Base(path)); err != nil {
		return err
	}

	if err = verify(path); err != nil {
		// NOREPLACE proved this destination absent at publication. Roll back that
		// newly published name through the original parent, never a replaced path.
		removed := unix.Unlinkat(int(dir.Fd()), filepath.Base(path), 0)
		if removed != nil && !errors.Is(removed, unix.ENOENT) {
			return errors.Join(err, removed)
		}
		if synced := dir.Sync(); synced != nil {
			return errors.Join(err, synced)
		}
		var final unix.Stat_t
		if present := unix.Fstatat(int(dir.Fd()), filepath.Base(path), &final, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(present, unix.ENOENT) {
			return errors.Join(err, store.CodeError("owner_runtime_rollback_unconfirmed"))
		}
		return err
	}

	if err = dir.Sync(); err != nil {
		return err
	}
	if hooks.after != nil {
		return hooks.after()
	}
	return nil
}

func loadOriginalOwnerKeys(state string, required []store.OriginalOwnerState) (*originalOwnerKeys, error) {
	dir, err := ownerPrivateDirectory(state)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "keys.json")
	body, err := readOwnerPrivateFile(path, 1024)
	if errors.Is(err, os.ErrNotExist) {
		if len(required) != 0 {
			return nil, store.CodeError("original_owner_key_lost")
		}
		seed, wrap := make([]byte, 32), make([]byte, 32)
		if _, err = rand.Read(seed); err != nil {
			return nil, err
		}
		if _, err = rand.Read(wrap); err != nil {
			return nil, err
		}
		if err = createOwnerPrivateFile(path, originalOwnerKeyFile{Version: 1, SignSeed: hex.EncodeToString(seed), WrappingKey: hex.EncodeToString(wrap)}); err != nil {
			return nil, err
		}
		body, err = readOwnerPrivateFile(path, 1024)
	}
	if err != nil {
		return nil, err
	}
	var value originalOwnerKeyFile
	if contract.DecodeBridgeStrict(body, &value) != nil || value.Version != 1 {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	canonical, _ := json.Marshal(value)
	if !bytes.Equal(append(canonical, '\n'), body) {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	seed, e1 := hex.DecodeString(value.SignSeed)
	wrap, e2 := hex.DecodeString(value.WrappingKey)
	if e1 != nil || e2 != nil || len(seed) != 32 || len(wrap) != 32 || hex.EncodeToString(seed) != value.SignSeed || hex.EncodeToString(wrap) != value.WrappingKey {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	key := ed25519.NewKeyFromSeed(seed)
	id, _ := contract.OriginalOwnerKeyID(key.Public().(ed25519.PublicKey))
	keys := &originalOwnerKeys{sign: key, wrap: wrap, id: id}
	for _, owner := range required {
		if owner.Intent.CoordinatorKeyID != id {
			return nil, store.CodeError("original_owner_key_changed")
		}
		// Also prove the independent wrapping key still opens this owner's exact
		// credential. A matching signing seed cannot hide wrapping-key drift.
		if _, err = keys.readLaunchCredential(state, owner.Intent.Scope); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

type originalOwnerCredential struct {
	Version     int    `json:"version"`
	ScopeSHA256 string `json:"scope_sha256"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

func ownerCredentialPath(state string, scope contract.OriginalOwnerScope) string {
	// Callers validate the normalized scope before accessing this path.
	hash, _ := ownerCredentialScopeHash(scope)
	return filepath.Join(state, "owner-runtime", "launch-v2-"+hash+".json")
}
func legacyOwnerCredentialPath(state string, scope contract.OriginalOwnerScope) string {
	sum := sha256.Sum256([]byte(scope.LaunchID))
	return filepath.Join(state, "owner-runtime", "launch-"+hex.EncodeToString(sum[:])+".json")
}
func (k *originalOwnerKeys) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.wrap)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (k *originalOwnerKeys) credentialAAD(scopeHash string) []byte {
	return []byte("original-owner-launch-credential-v1\x00" + k.id + "\x00" + scopeHash)
}

// Prepare allocates owner ID/incarnation only after this immutable launch
// credential has been persisted. Normalize those two non-launch fields.
func ownerCredentialScopeHash(scope contract.OriginalOwnerScope) (string, error) {
	scope.OwnerID, scope.Incarnation = "launch-credential", 1
	return contract.OriginalOwnerScopeSHA256(scope)
}
func (k *originalOwnerKeys) saveLaunchCredential(state string, scope contract.OriginalOwnerScope, token string) error {
	if token == "" || len(token) > 1024 {
		return store.CodeError("original_owner_credential_invalid")
	}
	scopeHash, err := ownerCredentialScopeHash(scope)
	if err != nil {
		return err
	}
	path := ownerCredentialPath(state, scope)
	if _, err := os.Lstat(path); err == nil {
		value, old, err := k.readLaunchCredentialFile(path)
		if err != nil {
			return err
		}
		if value.ScopeSHA256 != scopeHash || old != token {
			return store.ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacy := legacyOwnerCredentialPath(state, scope)
	if _, err = os.Lstat(legacy); err == nil {
		value, old, e := k.readLaunchCredentialFile(legacy)
		if e != nil {
			return e
		}
		if value.ScopeSHA256 == scopeHash {
			if old != token {
				return store.ErrConflict
			}
			// Preserve ciphertext and AAD; exclusive publication leaves v1 intact.
			return createOwnerPrivateFile(path, value)
		}
		// A valid predecessor scope is never authority for this new scope. The
		// authenticated caller supplied token independently; keep v1 untouched.
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	aead, err := k.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nil, nonce, []byte(token), k.credentialAAD(scopeHash))
	return createOwnerPrivateFile(path, originalOwnerCredential{Version: 1, ScopeSHA256: scopeHash, Nonce: hex.EncodeToString(nonce), Ciphertext: hex.EncodeToString(sealed)})
}
func (k *originalOwnerKeys) readLaunchCredential(state string, scope contract.OriginalOwnerScope) (string, error) {
	hash, err := ownerCredentialScopeHash(scope)
	if err != nil {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	path := ownerCredentialPath(state, scope)
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		path = legacyOwnerCredentialPath(state, scope)
	} else if err != nil {
		return "", store.CodeError("original_owner_credential_unavailable")
	}
	value, token, err := k.readLaunchCredentialFile(path)
	if err != nil {
		return "", err
	}
	if value.ScopeSHA256 != hash {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	return token, nil
}

// Authenticate even a legacy predecessor's declared scope before considering
// it for migration. Callers separately require the expected scope for reads.
func (k *originalOwnerKeys) readLaunchCredentialFile(path string) (originalOwnerCredential, string, error) {
	var value originalOwnerCredential
	body, err := readOwnerPrivateFile(path, 4096)
	if err != nil {
		return value, "", store.CodeError("original_owner_credential_unavailable")
	}
	if contract.DecodeBridgeStrict(body, &value) != nil || value.Version != 1 {
		return value, "", store.CodeError("original_owner_credential_invalid")
	}
	canonical, _ := json.Marshal(value)
	hash := value.ScopeSHA256
	hashBytes, err := hex.DecodeString(hash)
	if err != nil || len(hashBytes) != 32 || hex.EncodeToString(hashBytes) != hash || !bytes.Equal(append(canonical, '\n'), body) {
		return value, "", store.CodeError("original_owner_credential_invalid")
	}
	nonce, e1 := hex.DecodeString(value.Nonce)
	sealed, e2 := hex.DecodeString(value.Ciphertext)
	aead, err := k.aead()
	if e1 != nil || e2 != nil || err != nil || len(nonce) != aead.NonceSize() || len(sealed) > 1040 {
		return value, "", store.CodeError("original_owner_credential_invalid")
	}
	plain, err := aead.Open(nil, nonce, sealed, k.credentialAAD(hash))
	if err != nil || len(plain) == 0 || len(plain) > 1024 {
		return value, "", store.CodeError("original_owner_credential_invalid")
	}
	return value, string(plain), nil
}
