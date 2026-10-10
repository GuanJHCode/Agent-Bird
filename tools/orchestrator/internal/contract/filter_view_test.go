package contract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func filterViewFixture(t *testing.T) (FilterViewEvidenceV1, GitFilterManifestV1) {
	t.Helper()
	m := filterManifestFixture()
	h, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	v := FilterViewEvidenceV1{Version: 1, ManifestSHA256: h, FilterContextSHA256: strings.Repeat("c", 64), Views: []FilterPathViewV1{}}
	for _, path := range m.Paths {
		v.Views = append(v.Views, FilterPathViewV1{path, strings.Repeat("b", 64), strings.Repeat("d", 64)})
	}
	return v, m
}

func TestFilterViewEvidenceContract(t *testing.T) {
	t.Run("binding_and_copy", func(t *testing.T) {
		v, m := filterViewFixture(t)
		if v.Validate() != nil || v.ValidateBinding(m, v.FilterContextSHA256) != nil {
			t.Fatal("valid view refused")
		}
		b, _ := json.Marshal(v)
		var decoded FilterViewEvidenceV1
		if err := json.Unmarshal(b, &decoded); err != nil || !reflect.DeepEqual(v, decoded) {
			t.Fatal("round trip", err)
		}
		copy := v.Clone()
		copy.Views[0].RenderedSHA256 = strings.Repeat("e", 64)
		if copy.Views[0] == v.Views[0] {
			t.Error("clone shares views")
		}
		if v.ValidateBinding(m, strings.Repeat("f", 64)) == nil {
			t.Error("foreign live context accepted")
		}
		m.Paths = []string{"a.txt"}
		if v.ValidateBinding(m, v.FilterContextSHA256) == nil {
			t.Error("foreign manifest accepted")
		}
		v, m = filterViewFixture(t)
		v.Views = v.Views[:1]
		if v.ValidateBinding(m, v.FilterContextSHA256) == nil {
			t.Error("missing path accepted")
		}
	})
	t.Run("strict_wire", func(t *testing.T) {
		v, _ := filterViewFixture(t)
		encoded, _ := json.Marshal(v)
		raw := string(encoded)
		bad := []string{
			strings.Replace(raw, `"version":1`, `"version":1,"version":1`, 1),
			strings.Replace(raw, `"version":1`, `"Version":1`, 1),
			strings.Replace(raw, `"version":1`, `"version":1,"unknown":1`, 1),
			strings.Replace(raw, `"version":1,`, ``, 1),
			strings.Replace(raw, `"views":[`, `"views":null,"ignored":[`, 1),
			strings.Replace(raw, `"path":"a.txt"`, `"path":"a.txt","path":"a.txt"`, 1),
			strings.Replace(raw, `"path":"a.txt"`, `"Path":"a.txt"`, 1),
			strings.Replace(raw, `"path":"a.txt",`, ``, 1),
			strings.Replace(raw, `"path":"a.txt"`, `"path":null`, 1),
			`null`, raw + ` {}`, strings.Repeat(" ", FilterViewMaximumBytes) + raw,
		}
		for i, input := range bad {
			var out FilterViewEvidenceV1
			if json.Unmarshal([]byte(input), &out) == nil {
				t.Errorf("malformed wire %d accepted", i)
			}
		}
	})
	t.Run("bounds_and_shape", func(t *testing.T) {
		mutations := []func(*FilterViewEvidenceV1){
			func(v *FilterViewEvidenceV1) { v.Version = 2 },
			func(v *FilterViewEvidenceV1) { v.ManifestSHA256 = strings.Repeat("A", 64) },
			func(v *FilterViewEvidenceV1) { v.FilterContextSHA256 = "" },
			func(v *FilterViewEvidenceV1) { v.Views = nil },
			func(v *FilterViewEvidenceV1) { v.Views = []FilterPathViewV1{} },
			func(v *FilterViewEvidenceV1) { v.Views[0].CanonicalSHA256 = "bad" },
			func(v *FilterViewEvidenceV1) { v.Views[0].RenderedSHA256 = "" },
			func(v *FilterViewEvidenceV1) { v.Views[0].Path = "../a" },
			func(v *FilterViewEvidenceV1) { v.Views[1].Path = "a.txt" },
			func(v *FilterViewEvidenceV1) { v.Views[0].Path = "A.txt"; v.Views[1].Path = "a.txt" },
			func(v *FilterViewEvidenceV1) { v.Views[0], v.Views[1] = v.Views[1], v.Views[0] },
			func(v *FilterViewEvidenceV1) {
				p := v.Views[0]
				v.Views = make([]FilterPathViewV1, 257)
				for i := range v.Views {
					v.Views[i] = p
					v.Views[i].Path = fmt.Sprintf("%03d", i)
				}
			},
			func(v *FilterViewEvidenceV1) {
				p := v.Views[0]
				v.Views = make([]FilterPathViewV1, 256)
				for i := range v.Views {
					v.Views[i] = p
					v.Views[i].Path = fmt.Sprintf("%03d/", i) + strings.Repeat("x", 508)
				}
			},
		}
		for i, change := range mutations {
			v, _ := filterViewFixture(t)
			change(&v)
			if v.Validate() == nil {
				t.Errorf("invalid shape %d accepted", i)
			}
		}
	})
}
