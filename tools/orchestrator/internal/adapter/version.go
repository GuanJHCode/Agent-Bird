package adapter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parsedVersion is the normalized stable triple used for range checks.
// The original --version text stays on the binary pin and is never replaced.
type parsedVersion struct {
	Major      int
	Minor      int
	Patch      int
	Product    string
	Prerelease string
	Build      string
	Note       string
	Channel    string
	Normalized string
}

var versionLine = regexp.MustCompile(`^(?:(codex-cli|claude-code|claude|grok|agy|antigravity-cli|antigravity)\s+)?v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z][0-9A-Za-z.-]*))?(?:\+([0-9A-Za-z.-]+))?(?:\s+\(([^)]+)\))?(?:\s+\[([A-Za-z][0-9A-Za-z.-]*)\])?$`)

func parseCLIVersion(raw string) (parsedVersion, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, "\r\n\x00") {
		return parsedVersion{}, errVersionUnparsed
	}
	match := versionLine.FindStringSubmatch(raw)
	if match == nil {
		return parsedVersion{}, errVersionUnparsed
	}
	major, minor, patch, err := decimalTriple(match[2], match[3], match[4])
	if err != nil {
		return parsedVersion{}, err
	}
	parsed := parsedVersion{
		Major: major, Minor: minor, Patch: patch,
		Product: match[1], Prerelease: match[5], Build: match[6],
		Note: strings.TrimSpace(match[7]), Channel: strings.ToLower(match[8]),
		Normalized: fmt.Sprintf("%d.%d.%d", major, minor, patch),
	}
	return parsed, nil
}

func decimalTriple(major, minor, patch string) (int, int, int, error) {
	values := [3]int{}
	for i, part := range []string{major, minor, patch} {
		number, err := strconv.Atoi(part)
		if err != nil || strconv.Itoa(number) != part {
			return 0, 0, 0, errVersionUnparsed
		}
		values[i] = number
	}
	return values[0], values[1], values[2], nil
}

func (v parsedVersion) compare(other parsedVersion) int {
	if v.Major != other.Major {
		return cmpInt(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return cmpInt(v.Minor, other.Minor)
	}
	return cmpInt(v.Patch, other.Patch)
}

func cmpInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func releaseClass(provider Provider, v parsedVersion) (string, string) {
	if v.Prerelease != "" || v.Build != "" || channelWord(v.Channel) || noteHasChannel(v.Note) {
		return "prerelease", "cli_version_prerelease"
	}
	switch v.Channel {
	case "", "stable", "release", "ga", "latest":
	default:
		return "unparsed", "cli_version_unparsed"
	}
	switch noteClass(provider, v.Note) {
	case "prerelease":
		return "prerelease", "cli_version_prerelease"
	case "unrecognized":
		return "unrecognized", "cli_version_unrecognized_note"
	default:
		return "stable", ""
	}
}

// noteClass allows only the annotations each provider actually prints for a
// stable build. Any other parenthetical text stays out of the stable window.
func noteClass(provider Provider, note string) string {
	if note == "" {
		return "stable"
	}
	if noteHasChannel(note) {
		return "prerelease"
	}
	if knownStableNote(provider, note) {
		return "stable"
	}
	return "unrecognized"
}

func noteHasChannel(note string) bool {
	token := note
	for i, r := range note {
		if r == ' ' || r == '.' || r == '/' {
			token = note[:i]
			break
		}
	}
	return channelWord(token)
}

func knownStableNote(provider Provider, note string) bool {
	switch provider {
	case ProviderClaude:
		return note == "Claude Code"
	case ProviderGrok:
		return grokCommitNote.MatchString(note)
	default:
		return false
	}
}

var grokCommitNote = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func channelWord(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "nightly", "dev", "devel", "development", "alpha", "beta", "rc", "pre", "preview", "canary", "insiders", "next":
		return true
	default:
		return false
	}
}

func prefixAllowed(provider Provider, product string) bool {
	if product == "" {
		return true
	}
	switch provider {
	case ProviderClaude:
		return product == "claude" || product == "claude-code"
	case ProviderAGY:
		return product == "agy" || product == "antigravity" || product == "antigravity-cli"
	case ProviderGrok:
		return product == "grok"
	case ProviderCodex:
		return product == "codex-cli"
	default:
		return false
	}
}
