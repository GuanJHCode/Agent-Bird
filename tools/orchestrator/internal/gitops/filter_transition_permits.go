package gitops

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

// TransformPermits derives exact byte lengths and hashes from the live readonly
// target/candidate owners. It cannot import authority from a serialized receipt.
func (t *FilterTargetTransition) TransformPermits(ctx context.Context) ([]gitfilter.ExactTransformPermit, error) {
	if !t.enter() {
		return nil, fail(CodeIdentityChanged, "filter permits owner", nil)
	}
	defer t.mu.Unlock()
	if err := t.verifyInitial(ctx); err != nil {
		return nil, err
	}
	m := t.pair.target
	tree, err := filterReadTree(ctx, m.source, m.candidate.CandidateOID)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, e := range tree {
		sizes[e.path] = e.size
	}
	baseSizes := map[string]int64{}
	for _, e := range m.base {
		baseSizes[e.path] = e.size
	}
	var out []gitfilter.ExactTransformPermit
	add := func(p gitfilter.ExactTransformPermit) error {
		for i := range out {
			q := &out[i]
			if q.Path == p.Path && q.Operation == p.Operation && q.InputSHA256 == p.InputSHA256 && q.InputBytes == p.InputBytes {
				if q.OutputSHA256 != p.OutputSHA256 || q.OutputBytes != p.OutputBytes {
					return fail(CodeReviewMismatch, "filter permits ambiguous", nil)
				}
				q.MaximumCalls += p.MaximumCalls
				return nil
			}
		}
		out = append(out, p)
		return nil
	}
	for _, v := range m.receipt.BaseFilter.Views {
		r, ok := m.expected[v.Path]
		n, nok := baseSizes[v.Path]
		if !ok || !nok || r.SHA256 != v.RenderedSHA256 {
			return nil, fail(CodeIdentityChanged, "filter base permit", nil)
		}
		if err = add(gitfilter.ExactTransformPermit{Path: v.Path, Operation: "clean", InputSHA256: v.RenderedSHA256, OutputSHA256: v.CanonicalSHA256, InputBytes: r.Size, OutputBytes: n, MaximumCalls: 2}); err != nil {
			return nil, err
		}
	}
	for _, v := range m.candidate.Filter.Views {
		r, ok := t.expected[v.Path]
		n, nok := sizes[v.Path]
		if !ok || !nok || r.SHA256 != v.RenderedSHA256 {
			return nil, fail(CodeIdentityChanged, "filter candidate permit", nil)
		}
		for _, p := range []gitfilter.ExactTransformPermit{
			{Path: v.Path, Operation: "clean", InputSHA256: v.RenderedSHA256, OutputSHA256: v.CanonicalSHA256, InputBytes: r.Size, OutputBytes: n, MaximumCalls: 1},
			{Path: v.Path, Operation: "smudge", InputSHA256: v.CanonicalSHA256, OutputSHA256: v.RenderedSHA256, InputBytes: n, OutputBytes: r.Size, MaximumCalls: 1},
		} {
			if err = add(p); err != nil {
				return nil, err
			}
		}
	}
	if err = t.verifyInitial(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
