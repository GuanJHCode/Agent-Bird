package gitops

import (
	"strings"
	"testing"
)

func TestFilteredCandidateChangeShape(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name    string
		changes []CandidateChange
		valid   bool
	}{
		{"regular", []CandidateChange{{Path: "a.txt", Mode: "100644", BlobOID: oid}}, true},
		{"delete", []CandidateChange{{Path: "a.txt", Deleted: true}}, true},
		{"delete_mode", []CandidateChange{{Path: "a.txt", Deleted: true, Mode: "100644"}}, false},
		{"delete_blob", []CandidateChange{{Path: "a.txt", Deleted: true, BlobOID: oid}}, false},
		{"foreign_mode", []CandidateChange{{Path: "a.txt", Mode: "120000", BlobOID: oid}}, false},
		{"unsorted", []CandidateChange{{Path: "b", Deleted: true}, {Path: "a", Deleted: true}}, false},
		{"file_directory_alias", []CandidateChange{{Path: "a", Deleted: true}, {Path: "a/b", Mode: "100644", BlobOID: oid}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFilteredChanges(tc.changes)
			if (err == nil) != tc.valid || err != nil && !IsCode(err, CodeReviewMismatch) {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
