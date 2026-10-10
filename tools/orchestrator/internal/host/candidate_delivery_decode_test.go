package host

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestDecodeCandidateDeliveryStrict(t *testing.T) {
	legacy := candidateDeliveryDecodeFixture(nil)
	filter := &gitops.FilterViewEvidenceV1{Version: 1, ManifestSHA256: strings.Repeat("a", 64), FilterContextSHA256: strings.Repeat("b", 64), Views: []gitops.FilterPathViewV1{{Path: "a.txt", CanonicalSHA256: strings.Repeat("c", 64), RenderedSHA256: strings.Repeat("d", 64)}}}
	filtered := candidateDeliveryDecodeFixture(filter)
	for _, tc := range []struct {
		name string
		body func() []byte
		want CandidateDelivery
		ok   bool
	}{
		{"legacy_nil_filter", func() []byte {
			return []byte(strings.Replace(string(candidateDeliveryDecodeJSON(t, legacy)), `"candidate":{`, `"candidate":{"filter":null,`, 1))
		}, legacy, true},
		{"filter_attachment", func() []byte { return candidateDeliveryDecodeJSON(t, filtered) }, filtered, true},
		{"unknown_candidate_field", func() []byte {
			return []byte(strings.Replace(string(candidateDeliveryDecodeJSON(t, legacy)), `"candidate":{`, `"candidate":{"foreign":true,`, 1))
		}, CandidateDelivery{}, false},
		{"duplicate_filter_attachment", func() []byte {
			return []byte(strings.Replace(string(candidateDeliveryDecodeJSON(t, filtered)), `"candidate":{`, `"candidate":{"filter":null,`, 1))
		}, CandidateDelivery{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeCandidateDeliveryStrict(tc.body())
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
			if tc.ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func candidateDeliveryDecodeFixture(filter *gitops.FilterViewEvidenceV1) CandidateDelivery {
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
	return CandidateDelivery{Version: 1, Candidate: gitops.CandidateReceipt{Binding: &binding, CandidateOID: strings.Repeat("1", 40), TreeOID: strings.Repeat("2", 40), PrivateRef: "refs/orchestrator/g3/test", Filter: filter}}
}

func candidateDeliveryDecodeJSON(t *testing.T, value CandidateDelivery) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
