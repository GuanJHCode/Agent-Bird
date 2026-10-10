package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func filterManifestFixture() GitFilterManifestV1 {
	h := strings.Repeat("a", 64)
	return GitFilterManifestV1{1, "native_filter", []string{"a.txt", "dir/b.txt"}, GitFilterGitPin{"/usr/bin/git", "git version 2", h}, GitFilterToolPin{"/private/sealed/clean", h, []string{}}, GitFilterToolPin{"/private/sealed/smudge", h, []string{"--stream"}}, []GitFilterAttributePin{{".gitattributes", h}}, h}
}
func filterGrantFixture() GitFilterResourceGrant {
	h := strings.Repeat("a", 64)
	return GitFilterResourceGrant{1, "filter_1", h, h, h, h, 32 << 20, 16 << 20, 3}
}
func filterCleanupFixture() GitFilterCleanup {
	g := filterGrantFixture()
	return GitFilterCleanup{g.Version, g.ReservationID, g.ScopeSHA256, g.ManifestSHA256, g.CleanToolSHA256, g.SmudgeToolSHA256, strings.Repeat("b", 64), 0}
}

func TestGitFilterManifestValidation(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if err := filterManifestFixture().Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("independent_limits", func(t *testing.T) {
		m := filterManifestFixture()
		m.DriverID = strings.Repeat("x", 64)
		m.Paths = []string{strings.Repeat("p", 512)}
		m.Git.Path = "/" + strings.Repeat("g", 4095)
		m.Git.Version = strings.Repeat("v", 256)
		m.Clean.Args = make([]string, 16)
		m.Clean.Args[0] = strings.Repeat("a", 4096)
		m.Smudge.Args = []string{strings.Repeat("b", 4096)}
		m.Attributes = []GitFilterAttributePin{}
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		m.DriverID += "x"
		if m.Validate() == nil {
			t.Fatal("65-byte driver accepted")
		}
		m = filterManifestFixture()
		m.Attributes = make([]GitFilterAttributePin, 257)
		if m.Validate() == nil {
			t.Fatal("attribute count accepted")
		}
		m = filterManifestFixture()
		m.Attributes = []GitFilterAttributePin{{"A/.gitattributes", m.ConfigSHA256}, {"a/.gitattributes", m.ConfigSHA256}}
		if m.Validate() == nil {
			t.Fatal("attribute case alias accepted")
		}
	})
	cases := []struct {
		name   string
		change func(*GitFilterManifestV1)
	}{
		{"version", func(m *GitFilterManifestV1) { m.Version = 2 }},
		{"driver", func(m *GitFilterManifestV1) { m.DriverID = "LfS" }},
		{"driver_chars", func(m *GitFilterManifestV1) { m.DriverID = "x.y" }},
		{"empty_paths", func(m *GitFilterManifestV1) { m.Paths = []string{} }},
		{"paths_limit", func(m *GitFilterManifestV1) { m.Paths = make([]string, 257) }},
		{"order", func(m *GitFilterManifestV1) { m.Paths = []string{"z", "a"} }},
		{"duplicate", func(m *GitFilterManifestV1) { m.Paths = []string{"a", "a"} }},
		{"case_alias", func(m *GitFilterManifestV1) { m.Paths = []string{"A", "a"} }},
		{"path_bytes", func(m *GitFilterManifestV1) { m.Paths = []string{strings.Repeat("é", 257)} }},
		{"git_component", func(m *GitFilterManifestV1) { m.Paths = []string{"a/.GiT/data"} }},
		{"traversal", func(m *GitFilterManifestV1) { m.Paths = []string{"a/../b"} }},
		{"backslash", func(m *GitFilterManifestV1) { m.Paths = []string{"a\\b"} }},
		{"not_clean", func(m *GitFilterManifestV1) { m.Paths = []string{"a//b"} }},
		{"control", func(m *GitFilterManifestV1) { m.Paths = []string{"a\nb"} }},
		{"invalid_utf8", func(m *GitFilterManifestV1) { m.Paths = []string{string([]byte{0xff})} }},
		{"git_path", func(m *GitFilterManifestV1) { m.Git.Path = "/usr/../bin/git" }},
		{"tool_path", func(m *GitFilterManifestV1) { m.Clean.Path = "clean" }},
		{"tool_path_bytes", func(m *GitFilterManifestV1) { m.Clean.Path = "/" + strings.Repeat("a", 4096) }},
		{"git_version", func(m *GitFilterManifestV1) { m.Git.Version = strings.Repeat("é", 129) }},
		{"digest", func(m *GitFilterManifestV1) { m.ConfigSHA256 = strings.Repeat("A", 64) }},
		{"nil_args", func(m *GitFilterManifestV1) { m.Clean.Args = nil }},
		{"args_count", func(m *GitFilterManifestV1) { m.Clean.Args = make([]string, 17) }},
		{"args_bytes", func(m *GitFilterManifestV1) { m.Clean.Args = []string{strings.Repeat("a", 4097)} }},
		{"args_expansion", func(m *GitFilterManifestV1) { m.Smudge.Args = []string{"--path=%f"} }},
		{"args_control", func(m *GitFilterManifestV1) { m.Clean.Args = []string{"a\x00b"} }},
		{"nil_attributes", func(m *GitFilterManifestV1) { m.Attributes = nil }},
		{"attributes_name", func(m *GitFilterManifestV1) { m.Attributes[0].Path = ".GITATTRIBUTES" }},
		{"attributes_order", func(m *GitFilterManifestV1) { m.Attributes = append(m.Attributes, m.Attributes[0]) }},
		{"attributes_overlap", func(m *GitFilterManifestV1) { m.Paths = []string{".gitattributes"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := filterManifestFixture()
			c.change(&m)
			if m.Validate() == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestGitFilterStrictWire(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		target func() any
	}{
		{"manifest", filterManifestFixture(), func() any { return new(GitFilterManifestV1) }},
		{"git", filterManifestFixture().Git, func() any { return new(GitFilterGitPin) }},
		{"tool", filterManifestFixture().Clean, func() any { return new(GitFilterToolPin) }},
		{"attribute", filterManifestFixture().Attributes[0], func() any { return new(GitFilterAttributePin) }},
		{"grant", filterGrantFixture(), func() any { return new(GitFilterResourceGrant) }},
		{"cleanup", filterCleanupFixture(), func() any { return new(GitFilterCleanup) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(c.value)
			if err := json.Unmarshal(raw, c.target()); err != nil {
				t.Fatalf("valid: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(fields))
			for field := range fields {
				keys = append(keys, field)
			}
			for _, field := range keys {
				for _, null := range []bool{false, true} {
					copyFields := make(map[string]json.RawMessage, len(fields))
					for k, v := range fields {
						copyFields[k] = v
					}
					if null {
						copyFields[field] = json.RawMessage("null")
					} else {
						delete(copyFields, field)
					}
					bad, _ := json.Marshal(copyFields)
					if json.Unmarshal(bad, c.target()) == nil {
						t.Fatalf("accepted missing/null %s null=%v", field, null)
					}
				}
				value := fields[field]
				bad := append([]byte{}, raw[:len(raw)-1]...)
				bad = append(bad, []byte(fmt.Sprintf(",%q:%s}", field, value))...)
				if json.Unmarshal(bad, c.target()) == nil {
					t.Fatalf("accepted duplicate %s", field)
				}
				delete(fields, field)
				fields[strings.ToUpper(field)] = value
				bad, _ = json.Marshal(fields)
				if json.Unmarshal(bad, c.target()) == nil {
					t.Fatalf("accepted case alias %s", field)
				}
				delete(fields, strings.ToUpper(field))
				fields[field] = value
			}
			fields["unknown"] = json.RawMessage("1")
			bad, _ := json.Marshal(fields)
			if json.Unmarshal(bad, c.target()) == nil {
				t.Fatal("accepted unknown")
			}
		})
	}
	t.Run("nested_null_arg", func(t *testing.T) {
		raw, _ := json.Marshal(filterManifestFixture())
		raw = []byte(strings.Replace(string(raw), `"args":[]`, `"args":[null]`, 1))
		if json.Unmarshal(raw, new(GitFilterManifestV1)) == nil {
			t.Fatal("null arg accepted")
		}
	})
}

func TestGitFilterResourceValidation(t *testing.T) {
	t.Run("valid_grant", func(t *testing.T) {
		if err := filterGrantFixture().Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("grant_boundaries", func(t *testing.T) {
		for _, mutate := range []func(*GitFilterResourceGrant){func(g *GitFilterResourceGrant) { g.ReservationID = "../foreign" }, func(g *GitFilterResourceGrant) { g.ToolMaximumBytes-- }, func(g *GitFilterResourceGrant) { g.StreamMaximumBytes++ }, func(g *GitFilterResourceGrant) { g.StreamSlots++ }, func(g *GitFilterResourceGrant) { g.ManifestSHA256 = "bad" }} {
			g := filterGrantFixture()
			mutate(&g)
			if g.Validate() == nil {
				t.Fatal("invalid grant accepted")
			}
		}
	})
	t.Run("cleanup_boundaries", func(t *testing.T) {
		for _, n := range []int64{0, 112 << 20} {
			c := filterCleanupFixture()
			c.RemovedBytes = n
			if err := c.Validate(); err != nil {
				t.Fatalf("valid %d: %v", n, err)
			}
		}
		for _, n := range []int64{-1, (112 << 20) + 1} {
			c := filterCleanupFixture()
			c.RemovedBytes = n
			if c.Validate() == nil {
				t.Fatalf("invalid %d accepted", n)
			}
		}
	})
}

func TestGitFilterDigestCloneScope(t *testing.T) {
	m := filterManifestFixture()
	raw, _ := json.Marshal(m)
	want := sha256.Sum256(raw)
	digest, err := m.Digest()
	if err != nil || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest %q %v", digest, err)
	}
	clone := m.Clone()
	if !reflect.DeepEqual(m, clone) {
		t.Fatal("clone changed shape")
	}
	clone.Paths[0] = "other"
	clone.Clean.Args = append(clone.Clean.Args, "extra")
	clone.Smudge.Args[0] = "other"
	clone.Attributes[0].SHA256 = strings.Repeat("c", 64)
	if !reflect.DeepEqual(m, filterManifestFixture()) {
		t.Fatal("clone shares slices")
	}
	withArgs := m.Clone()
	withArgs.Clean.Args = []string{"--clean"}
	second := withArgs.Clone()
	second.Clean.Args[0] = "changed"
	if withArgs.Clean.Args[0] != "--clean" {
		t.Fatal("clean args shared")
	}
	zero := GitFilterManifestV1{}.Clone()
	if zero.Paths != nil || zero.Clean.Args != nil || zero.Smudge.Args != nil || zero.Attributes != nil {
		t.Fatal("clone changed nil shape")
	}
	c := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 2}
	host, _ := HostArtifactScopeSHA256(c)
	scopeRaw, _ := json.Marshal(struct {
		Version        int
		HostScope      string
		PlanRevision   int
		ManifestSHA256 string
	}{1, host, 2, digest})
	wantScope := sha256.Sum256(append([]byte("git-filter-resource-v1\x00"), scopeRaw...))
	scope, err := GitFilterResourceScopeSHA256(c, digest)
	if err != nil || scope != hex.EncodeToString(wantScope[:]) {
		t.Fatalf("scope %q %v", scope, err)
	}
	for _, change := range []func(*LaunchCommand){func(c *LaunchCommand) { c.PlanRevision = 0 }, func(c *LaunchCommand) { c.HostID = "" }} {
		bad := c
		change(&bad)
		if _, err := GitFilterResourceScopeSHA256(bad, digest); err == nil {
			t.Fatal("bad scope accepted")
		}
	}
	c.ExecutionEpoch++
	same, _ := GitFilterResourceScopeSHA256(c, digest)
	if same != scope {
		t.Fatal("mutable epoch bound")
	}
	c.PlanRevision++
	different, _ := GitFilterResourceScopeSHA256(c, digest)
	if different == scope {
		t.Fatal("plan revision not bound")
	}
	if _, err := GitFilterResourceScopeSHA256(c, strings.ToUpper(digest)); err == nil {
		t.Fatal("noncanonical digest accepted")
	}
}

func TestGitFilterCanonicalSizeBoundary(t *testing.T) {
	m := filterManifestFixture()
	m.Paths = make([]string, 70)
	for i := range m.Paths {
		m.Paths[i] = fmt.Sprintf("%03d-%s", i, strings.Repeat("a", 400))
	}
	m.Clean.Args = []string{""}
	raw, _ := json.Marshal(m)
	fill := 32767 - len(raw)
	if fill < 0 || fill >= 4096 {
		t.Fatalf("bad boundary fixture %d", fill)
	}
	m.Clean.Args[0] = strings.Repeat("x", fill)
	raw, _ = json.Marshal(m)
	if len(raw)+1 != 32768 {
		t.Fatal("wrong exact size")
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Clean.Args[0] += "x"
	if m.Validate() == nil {
		t.Fatal("newline exceeded size accepted")
	}
}
