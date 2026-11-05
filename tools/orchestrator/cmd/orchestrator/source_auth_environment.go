package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"os"
	"sort"
	"strings"
)

// Version 1 commits to the source auth/config environment union for the four
// supported providers, including unknown suffixes in their credential families.
// This is not a claim about arbitrary future providers. No source values are stored.
type sourceAuthEnvironmentProof struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	SHA256    string `json:"sha256"`
}

const sourceAuthEnvironmentAlgorithm = "sha256-length-prefixed-source-auth-env-v1"

func sourceAuthEnvironmentName(name string) bool {
	switch name {
	case "CODEX_SQLITE_HOME", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CODEX_CI", "CODEX_VERSION", "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_TOOL_CALL_ID", "CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS":
		return false
	case "CLAUDE_CODE_SESSION_ID", "CLAUDE_PROJECT_DIR":
		// Claude injects these correlation/project context fields into stdio
		// MCP subprocesses after the controller captures its source environment.
		// Owner authority and workspace routing use their existing separate guards.
		return false
	case "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS":
		return true
	}
	// Include companion credentials and route/config locations when a supported
	// CLI selects a cloud route; preserving its selector alone could change auth.
	for _, prefix := range []string{
		"OPENAI_", "CODEX_", "ANTHROPIC_", "CLAUDE_", "GROK_", "XAI_", "AGY_", "ANTIGRAVITY_",
		"GEMINI_", "GOOGLE_", "GCLOUD_", "GCP_", "VERTEX_", "CLOUD_ML_", "AWS_", "AZURE_", "FOUNDRY_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func sourceAuthEnvironmentProjection(environment []string) (map[string]string, []string) {
	values := map[string]string{}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && sourceAuthEnvironmentName(name) {
			values[name] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return values, names
}

func sourceAuthEnvironmentPart(h hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

func sourceAuthEnvironment(environment []string) *sourceAuthEnvironmentProof {
	values, names := sourceAuthEnvironmentProjection(environment)
	h := sha256.New()
	sourceAuthEnvironmentPart(h, sourceAuthEnvironmentAlgorithm)
	for _, name := range names {
		sourceAuthEnvironmentPart(h, name)
		sourceAuthEnvironmentPart(h, values[name])
	}
	return &sourceAuthEnvironmentProof{Version: 1, Algorithm: sourceAuthEnvironmentAlgorithm, SHA256: hex.EncodeToString(h.Sum(nil))}
}

func verifySourceAuthEnvironment(proof *sourceAuthEnvironmentProof, environment []string) error {
	if proof == nil || proof.Version != 1 || proof.Algorithm != sourceAuthEnvironmentAlgorithm || len(proof.SHA256) != 64 {
		return codeError("mcp_source_environment_unverified")
	}
	expected, err := hex.DecodeString(proof.SHA256)
	if err != nil {
		return codeError("mcp_source_environment_unverified")
	}
	actual, _ := hex.DecodeString(sourceAuthEnvironment(environment).SHA256)
	if subtle.ConstantTimeCompare(expected, actual) != 1 {
		return codeError("mcp_source_environment_changed")
	}
	return nil
}

func currentMCPSourceEnvironment(ctx context.Context) error {
	_, managed, err := verifiedLocalOwner(ctx)
	if err != nil {
		return err
	}
	return verifySourceAuthEnvironment(managed.SourceAuthEnv, os.Environ())
}
