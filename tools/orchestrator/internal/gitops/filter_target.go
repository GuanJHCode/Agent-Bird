package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

// FilterTargetRequest is observation authority only, supplied by the original
// authenticated Host. This primitive does not authenticate an accepted review.
type FilterTargetRequest struct {
	Worktree, Ref, BaseOID                 string
	AttemptID                              string
	PlanRevision                           int
	FilterScopeSHA256, FilterReservationID string
}

type FilteredTargetObservationV1 struct {
	Version             int                  `json:"version"`
	AttemptID           string               `json:"attempt_id"`
	PlanRevision        int                  `json:"plan_revision"`
	TargetWorktree      string               `json:"target_worktree"`
	TargetRef           string               `json:"target_ref"`
	BaseOID             string               `json:"base_oid"`
	BaseTreeOID         string               `json:"base_tree_oid"`
	TargetIdentity      DirectoryIdentity    `json:"target_identity"`
	CommonIdentity      DirectoryIdentity    `json:"common_identity"`
	IndexIdentity       FileIdentity         `json:"index_identity"`
	IndexSHA256         string               `json:"index_sha256"`
	RefStateSHA256      string               `json:"ref_state_sha256"`
	IndexVectorSHA256   string               `json:"index_vector_sha256"`
	InventorySHA256     string               `json:"inventory_sha256"`
	CandidateOID        string               `json:"candidate_oid"`
	CandidateTreeOID    string               `json:"candidate_tree_oid"`
	CandidateSHA256     string               `json:"candidate_sha256"`
	CandidateBinding    CandidateBinding     `json:"candidate_binding"`
	OrderedInputOIDs    []string             `json:"ordered_input_oids"`
	FilterScopeSHA256   string               `json:"filter_scope_sha256"`
	FilterReservationID string               `json:"filter_reservation_id"`
	BaseFilter          FilterViewEvidenceV1 `json:"base_filter"`
}

type FilterTargetJournal interface {
	GitProcessJournal
	RecordFilteredTargetObservation(context.Context, FilteredTargetObservationV1) error
}

// FilterTargetObservation retains original descriptors and private live owners.
// It cannot be reconstructed from its observation receipt and owns no files.
type FilterTargetObservation struct {
	source                                    *FilterGitContext
	streams                                   FilterStreams
	streamBinding                             gitfilter.StreamBinding
	journal                                   FilterTargetJournal
	request                                   FilterTargetRequest
	candidate                                 CandidateReceipt
	parent, root, gitDir, index               *os.File
	parentIdentity, rootIdentity, gitIdentity DirectoryIdentity
	indexIdentity                             FileIdentity
	indexSHA                                  string
	base                                      []filterTreeEntry
	expected                                  map[string]filterTargetExpected
	inventory                                 map[string]filterTargetEntry
	refs                                      map[string]*filterPinnedFile
	configPin                                 filterPinnedFile
	receipt                                   FilteredTargetObservationV1
}

type filterTargetExpected struct {
	Mode, SHA256 string
	Size         int64
}
type filterTargetEntry struct {
	Identity FileIdentity
	SHA256   string
}

func InspectFilteredTarget(ctx context.Context, source *FilterGitContext, streams FilterStreams, req FilterTargetRequest, accepted CandidateReceipt, journal FilterTargetJournal) (*FilterTargetObservation, error) {
	if source == nil || source.source != nil || streams == nil || journal == nil || req.AttemptID == "" || req.AttemptID == accepted.AttemptID || accepted.Files != nil || req.PlanRevision < 1 || req.Worktree != source.repoRoot || req.Worktree != accepted.RepoRoot || req.BaseOID != accepted.BaseOID || req.PlanRevision != int(accepted.PlanRevision) || !filterTargetRef(req.Ref) || strings.TrimPrefix(req.Ref, "refs/heads/") == "" || strings.ContainsAny(req.Ref, "\x00\r\n") || len(req.Ref) > 512 {
		return nil, fail(CodeInvalidInput, "filter target request", nil)
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, fail(CodeInvalidInput, "filter target deadline", nil)
	}
	manifest, err := source.manifest.Digest()
	expected := gitfilter.StreamBinding{ManifestSHA256: manifest, ScopeSHA256: req.FilterScopeSHA256, ReservationID: req.FilterReservationID, AttemptID: req.AttemptID, PlanRevision: req.PlanRevision}
	if err != nil || req.FilterScopeSHA256 == "" || req.FilterReservationID == "" || streams.Binding() != expected {
		return nil, fail(CodeInvalidInput, "filter target stream binding", err)
	}
	m := &FilterTargetObservation{source: source, streams: streams, streamBinding: expected, journal: journal, request: req, candidate: cloneFilteredCandidate(accepted), expected: map[string]filterTargetExpected{}, refs: map[string]*filterPinnedFile{}}
	keep := false
	defer func() {
		if !keep {
			_ = m.Close()
		}
	}()
	if err = source.installTargetIndexGuard(); err != nil {
		return nil, err
	}
	ctx = source.Context(WithCandidateJournal(ctx, journal))
	// These checks must precede even candidate provenance queries: source
	// verification itself asks Git to read the target index for attributes.
	if err = validatePrivateRef(m.candidate.PrivateRef); err != nil {
		return nil, err
	}
	if requireOID(req.BaseOID) != nil || requireOID(m.candidate.CandidateOID) != nil {
		return nil, fail(CodeInvalidInput, "filter target object IDs", nil)
	}
	if err = m.open(); err != nil {
		return nil, err
	}
	if err = m.checkMetadata(); err != nil {
		return nil, err
	}
	if err = m.refFiles(true); err != nil {
		return nil, err
	}
	raw, identity, err := m.readIndex()
	if err != nil || filterTargetIndexHeader(raw) != nil {
		return nil, fail(CodeInvalidInput, "filter target index", err)
	}
	m.indexIdentity, m.indexSHA = identity, filterTargetHash(raw)
	m.configPin, err = pinFilterFileKind(filepath.Join(source.commonDir, "config"), "", filterConfigMaximumBytes, false, nil)
	if err != nil {
		return nil, err
	}
	if err = checkFilteredCandidateRef(ctx, source, m.candidate); err != nil {
		return nil, err
	}
	m.base, err = filterReadTree(ctx, source, req.BaseOID)
	if err != nil {
		return nil, err
	}
	if err = filterCheckTree(ctx, source, m.base); err != nil {
		return nil, err
	}
	baseTree, err := rev(ctx, req.Worktree, req.BaseOID+"^{tree}")
	if err != nil {
		return nil, err
	}
	vector, err := m.checkGit(ctx, baseTree)
	if err != nil {
		return nil, err
	}
	filterView := FilterViewEvidenceV1{Version: 1, ManifestSHA256: manifest, FilterContextSHA256: source.SHA256(), Views: []FilterPathViewV1{}}
	filtered := map[string]bool{}
	for _, p := range source.manifest.Paths {
		filtered[p] = true
	}
	var total int64
	for _, entry := range m.base {
		var sha string
		size := entry.size
		if !filtered[entry.path] {
			sha, err = filterReadBlob(ctx, source, entry, io.Discard)
		} else {
			var loaded gitfilter.StreamLoadResult
			loaded, err = streams.LoadStream(ctx, 0, func(out io.Writer) error { var e error; sha, e = filterReadBlob(ctx, source, entry, out); return e })
			if err != nil {
				return nil, err
			}
			if loaded.SHA256 != sha || loaded.Bytes != entry.size {
				return nil, fail(CodeIdentityChanged, "filter target raw slot", nil)
			}
			smudged, e := filterTransform(ctx, source, streams, "smudge", 0, 1)
			if e != nil {
				return nil, e
			}
			if smudged.InputSHA256 != sha || smudged.InputBytes != entry.size {
				return nil, fail(CodeIdentityChanged, "filter target smudge input", nil)
			}
			cleaned, e := filterTransform(ctx, source, streams, "clean", 1, 2)
			if e != nil {
				return nil, e
			}
			if cleaned.InputSHA256 != smudged.OutputSHA256 || cleaned.InputBytes != smudged.OutputBytes || cleaned.OutputSHA256 != sha || cleaned.OutputBytes != entry.size {
				return nil, fail(CodeTargetDirty, "filter target base roundtrip", nil)
			}
			filterView.Views = append(filterView.Views, FilterPathViewV1{Path: entry.path, CanonicalSHA256: sha, RenderedSHA256: smudged.OutputSHA256})
			sha, size = smudged.OutputSHA256, smudged.OutputBytes
		}
		if err != nil {
			return nil, err
		}
		total += size
		if size < 0 || size > 16<<20 || total > filterWorkspaceMaximumBytes {
			return nil, fail(CodeInvalidInput, "filter target rendered size", nil)
		}
		m.expected[entry.path] = filterTargetExpected{entry.mode, sha, size}
		for dir := path.Dir(entry.path); dir != "."; dir = path.Dir(dir) {
			m.expected[dir] = filterTargetExpected{Mode: "directory"}
		}
		if streams.Binding() != expected {
			return nil, fail(CodeIdentityChanged, "filter target stream owner", nil)
		}
	}
	if err = filterView.ValidateBinding(source.manifest, source.SHA256()); err != nil {
		return nil, err
	}
	m.inventory, err = m.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	candidateRaw, err := json.Marshal(m.candidate)
	if err != nil {
		return nil, err
	}
	inventoryRaw, err := json.Marshal(m.inventory)
	if err != nil {
		return nil, err
	}
	m.receipt = FilteredTargetObservationV1{Version: 1, AttemptID: req.AttemptID, PlanRevision: req.PlanRevision, TargetWorktree: req.Worktree, TargetRef: req.Ref, BaseOID: req.BaseOID, BaseTreeOID: baseTree, TargetIdentity: m.rootIdentity, CommonIdentity: source.commonIdentity, IndexIdentity: m.indexIdentity, IndexSHA256: m.indexSHA, IndexVectorSHA256: filterTargetHash(vector), InventorySHA256: filterTargetHash(inventoryRaw), CandidateOID: m.candidate.CandidateOID, CandidateTreeOID: m.candidate.TreeOID, CandidateSHA256: filterTargetHash(candidateRaw), CandidateBinding: *m.candidate.Binding, OrderedInputOIDs: append([]string(nil), m.candidate.OrderedInputOIDs...), FilterScopeSHA256: req.FilterScopeSHA256, FilterReservationID: req.FilterReservationID, BaseFilter: filterView}
	refState := map[string]*filterTargetEntry{}
	for p, pin := range m.refs {
		if pin == nil {
			refState[p] = nil
		} else {
			refState[p] = &filterTargetEntry{Identity: pin.identity, SHA256: pin.sha256}
		}
	}
	refsRaw, err := json.Marshal(refState)
	if err != nil {
		return nil, err
	}
	m.receipt.RefStateSHA256 = filterTargetHash(refsRaw)
	if err = m.Verify(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(m.receipt)
	if err != nil || len(body) > (1<<20)-1024 {
		return nil, fail(CodeInvalidInput, "filter target receipt size", err)
	}
	if err = journal.RecordFilteredTargetObservation(ctx, m.Receipt()); err != nil {
		return nil, err
	}
	if err = m.Verify(ctx); err != nil {
		return nil, err
	}
	keep = true
	return m, nil
}

func filterTargetHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (m *FilterTargetObservation) Receipt() FilteredTargetObservationV1 {
	out := m.receipt
	out.OrderedInputOIDs = append([]string(nil), out.OrderedInputOIDs...)
	out.BaseFilter = out.BaseFilter.Clone()
	return out
}
func (m *FilterTargetObservation) Close() error {
	var errs []error
	for _, p := range []**os.File{&m.index, &m.gitDir, &m.root, &m.parent} {
		if *p != nil {
			errs = append(errs, (*p).Close())
			*p = nil
		}
	}
	return errors.Join(errs...)
}
func (m *FilterTargetObservation) Verify(ctx context.Context) error {
	if m == nil || m.source == nil || m.streams == nil || m.journal == nil || m.streams.Binding() != m.streamBinding || m.receipt.Version != 1 {
		return fail(CodeIdentityChanged, "filter target live owner", nil)
	}
	ctx = m.source.Context(WithCandidateJournal(ctx, m.journal))
	if err := m.verifyAnchors(); err != nil {
		return err
	}
	if err := m.checkMetadata(); err != nil {
		return err
	}
	raw, id, err := m.readIndex()
	if err != nil || id != m.indexIdentity || filterTargetHash(raw) != m.indexSHA {
		return fail(CodeIdentityChanged, "filter target index changed", err)
	}
	vector, err := m.checkGit(ctx, m.receipt.BaseTreeOID)
	if err != nil {
		return err
	}
	if filterTargetHash(vector) != m.receipt.IndexVectorSHA256 {
		return fail(CodeIdentityChanged, "filter target index vector", nil)
	}
	got, err := m.snapshot(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, m.inventory) {
		return fail(CodeIdentityChanged, "filter target inventory changed", nil)
	}
	if err = checkFilteredCandidateRef(ctx, m.source, m.candidate); err != nil {
		return err
	}
	// Git/journal callbacks can run during any query, so finish with the
	// original held metadata and inventory once more, without new callbacks.
	if err = m.checkMetadata(); err != nil {
		return err
	}
	raw, id, err = m.readIndex()
	if err != nil || id != m.indexIdentity || filterTargetHash(raw) != m.indexSHA {
		return fail(CodeIdentityChanged, "filter target index changed", err)
	}
	got, err = m.snapshot(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, m.inventory) || m.streams.Binding() != m.streamBinding {
		return fail(CodeIdentityChanged, "filter target final inventory", nil)
	}
	if err = m.refFiles(false); err != nil {
		return err
	}
	if err = m.verifySourcePins(); err != nil {
		return err
	}
	return m.verifyAnchors()
}

// Restrict ref spelling before constructing any filesystem ref path. Git still
// verifies that the exact symbolic ref resolves to the requested base.
func filterTargetRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || !filterSafePath(ref) || strings.ContainsAny(ref, " ~^:?*[") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return false
		}
	}
	return true
}
