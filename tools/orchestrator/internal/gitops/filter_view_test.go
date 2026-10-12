package gitops

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFilterViewReceiptWireCompatibility(t *testing.T) {
	identity := `{"device":0,"inode":0,"mode":0,"uid":0}`
	cases := []struct {
		value  any
		legacy string
	}{
		{CandidateIntent{AttemptID: "a"}, `{"attempt_id":"a","repo_root":"","worktree":"","base_oid":"","private_ref":"","ordered_input_oids":null,"plan_revision":0,"created_at":"0001-01-01T00:00:00Z"}`},
		{CandidateReceipt{AttemptID: "a"}, `{"attempt_id":"a","repo_root":"","worktree":"","common_dir":"","base_oid":"","candidate_oid":"","tree_oid":"","ordered_input_oids":null,"root_identity":` + identity + `,"worktree_identity":"","repo_root_identity":` + identity + `,"common_dir_identity":` + identity + `,"files":null,"plan_revision":0,"private_ref":"","private_ref_expected_oid":""}`},
		{MaterializeReceipt{AttemptID: "a"}, `{"attempt_id":"a","repo_root":"","worktree":"","common_dir":"","commit_oid":"","tree_oid":"","base_oid":"","worktree_identity":"","root_identity":` + identity + `,"files":null,"ordered_input_oids":null,"plan_revision":0}`},
	}
	view := FilterViewEvidenceV1{Version: 1, ManifestSHA256: strings.Repeat("a", 64), FilterContextSHA256: strings.Repeat("b", 64), Views: []FilterPathViewV1{{Path: "a.txt", CanonicalSHA256: strings.Repeat("c", 64), RenderedSHA256: strings.Repeat("d", 64)}}}
	for _, tc := range cases {
		b, err := json.Marshal(tc.value)
		if err != nil || string(b) != tc.legacy {
			t.Fatalf("legacy %T bytes changed: %s %v", tc.value, b, err)
		}
		v := reflect.New(reflect.TypeOf(tc.value))
		v.Elem().Set(reflect.ValueOf(tc.value))
		v.Elem().FieldByName("Filter").Set(reflect.ValueOf(&view))
		b, err = json.Marshal(v.Interface())
		if err != nil {
			t.Fatal(err)
		}
		decoded := reflect.New(reflect.TypeOf(tc.value))
		if err = json.Unmarshal(b, decoded.Interface()); err != nil || !reflect.DeepEqual(v.Interface(), decoded.Interface()) {
			t.Fatal("nested evidence not retained", err)
		}
		bad := bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
		if err = json.Unmarshal(bad, decoded.Interface()); err == nil {
			t.Fatal("nested duplicate evidence accepted")
		}
	}
	receipt := CandidateReceipt{Filter: &view}
	artifact, err := CandidateArtifact("ok", receipt)
	if err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Candidate     CandidateReceipt `json:"candidate"`
		ReceiptSHA256 string           `json:"candidate_receipt_sha256"`
	}
	if err = json.Unmarshal(artifact, &delivery); err != nil || !reflect.DeepEqual(delivery.Candidate.Filter, &view) {
		t.Fatal("delivery dropped dual view", err)
	}
	before := delivery.ReceiptSHA256
	view.Views[0].RenderedSHA256 = strings.Repeat("e", 64)
	artifact, err = CandidateArtifact("ok", receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(artifact, &delivery); err != nil || delivery.ReceiptSHA256 == before {
		t.Fatal("receipt hash did not bind rendered evidence", err)
	}
}
