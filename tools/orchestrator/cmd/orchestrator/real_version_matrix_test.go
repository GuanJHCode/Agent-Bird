package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

// Real multi-version compatibility matrix.
//
// Opt-in only: ORCHESTRATOR_REAL_VERSION_MATRIX=1. Every selected case drives
// the full owner loop against the endpoint binary and an isolated,
// symlink-canonical state directory:
//
//	submit -> launch -> real tool read -> result artifact -> exit
//	       -> collect -> artifact verification -> ack -> accept -> final status
//
// Most endpoints use a task-local copy under the checkout. grok-ceiling and
// codex-ceiling have no copy there, so they run from a binary the operator
// supplies through ORCHESTRATOR_MATRIX_GROK_CEILING /
// ORCHESTRATOR_MATRIX_CODEX_CEILING; that path is recorded verbatim per round,
// because the batch requires each run's actual path, raw --version and digest.
// This is a real-model trial and deliberately not part of default automation.
//
// Cases run serially (no t.Parallel) so two providers never share a state
// directory, a socket, or a model session.
//
// This harness never rewrites a provider pin, provider authentication, a
// global install, or an existing lock. It only reads the pinned binary and
// writes inside its own temporary root and its own evidence directory.
const (
	matrixRunID       = "version-matrix"
	matrixTaskID      = "read"
	matrixWorkRev     = 1
	matrixMarkerFile  = "TOKEN.txt"
	matrixEvidenceRel = "tasks/cli-version-compatibility/data/real-matrix"
	matrixMaxActiveMS = 150000
	matrixSchemaVer   = 3
	matrixSpoolCap    = 40
	// matrixArtifactCap mirrors the spool's own limit in internal/events, so the
	// harness never reads an artifact the Host would have refused to register.
	matrixArtifactCap = 1 << 20
)

// matrixCase pins one endpoint binary. relBinary is repository-relative so the
// harness carries no operator-specific path; a binary that lives outside the
// checkout must be supplied through envOverride.
type matrixCase struct {
	name        string
	provider    adapter.Provider
	relBinary   string
	envOverride string
	grokWrite   bool
}

func matrixCases() []matrixCase {
	const pinned = "tasks/cli-version-compatibility/tmp"
	return []matrixCase{
		{"claude-baseline", adapter.ProviderClaude, pinned + "/npm/extract/claude-2.1.267/package/claude", "ORCHESTRATOR_MATRIX_CLAUDE_BASELINE", false},
		{"claude-ceiling", adapter.ProviderClaude, pinned + "/npm/extract/claude-2.1.316/package/claude", "ORCHESTRATOR_MATRIX_CLAUDE_CEILING", false},
		{"agy-baseline", adapter.ProviderAGY, pinned + "/binaries/agy-1.2.3/antigravity", "ORCHESTRATOR_MATRIX_AGY_BASELINE", false},
		{"agy-ceiling", adapter.ProviderAGY, pinned + "/binaries/agy-1.4.12/antigravity", "ORCHESTRATOR_MATRIX_AGY_CEILING", false},
		{"grok-baseline", adapter.ProviderGrok, pinned + "/binaries/grok-1.0.34/grok", "ORCHESTRATOR_MATRIX_GROK_BASELINE", true},
		{"grok-ceiling", adapter.ProviderGrok, "", "ORCHESTRATOR_MATRIX_GROK_CEILING", true},
		{"codex-baseline", adapter.ProviderCodex, pinned + "/binaries/codex-0.154.0/codex-aarch64-apple-darwin", "ORCHESTRATOR_MATRIX_CODEX_BASELINE", false},
		{"codex-ceiling", adapter.ProviderCodex, "", "ORCHESTRATOR_MATRIX_CODEX_CEILING", false},
	}
}

type matrixVersionView struct {
	Admitted   bool   `json:"admitted"`
	Normalized string `json:"normalized,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Baseline   string `json:"baseline,omitempty"`
	Ceiling    string `json:"ceiling,omitempty"`
	FrozenAt   string `json:"frozen_at,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type matrixGuard struct {
	Code          string   `json:"code"`
	Missing       []string `json:"missing,omitempty"`
	TrialComplete bool     `json:"trial_complete"`
}

type matrixEvent struct {
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	PayloadHash   string `json:"payload_hash"`
	ActionSlot    string `json:"action_slot"`
	AttemptID     string `json:"attempt_id"`
	SegmentID     string `json:"segment_id"`
	Kind          string `json:"kind"`
	Sequence      int64  `json:"sequence"`
}

type matrixArtifact struct {
	Path           string `json:"path"`
	DeclaredSize   int64  `json:"declared_size"`
	DeclaredSHA256 string `json:"declared_sha256"`
	ObservedSize   int64  `json:"observed_size"`
	ObservedSHA256 string `json:"observed_sha256"`
	SizeMatches    bool   `json:"size_matches"`
	DigestMatches  bool   `json:"digest_matches"`
	ExactMatch     bool   `json:"exact_match"`
}

type matrixSpoolEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Excerpt  string `json:"excerpt,omitempty"`
	Withheld string `json:"withheld,omitempty"`
}

// matrixDiagnostics attributes a prelaunch failure to a stage. The launch
// ledger collapses every non-allowlisted cause into "launch_failed", so these
// structural observations are what actually localizes the first failure.
type matrixDiagnostics struct {
	ReportCapabilityPresent  bool           `json:"report_capability_present"`
	PreparedEventPresent     bool           `json:"prepared_event_present"`
	SpawnedEventPresent      bool           `json:"spawned_event_present"`
	LaunchPhase              string         `json:"launch_phase,omitempty"`
	LaunchCauseCode          string         `json:"launch_cause_code,omitempty"`
	LaunchExecutable         string         `json:"launch_executable,omitempty"`
	LaunchExecutableRecorded bool           `json:"launch_executable_recorded"`
	ProcessExitCode          *int           `json:"process_exit_code,omitempty"`
	StateDirSymlinkPrefix    bool           `json:"state_dir_had_symlink_prefix"`
	CapabilityPathCheck      string         `json:"sandbox_capability_path_check,omitempty"`
	CapabilityRel            string         `json:"sandbox_capability_rel,omitempty"`
	HostLogClassification    string         `json:"host_log_classification,omitempty"`
	StopEvents               []string       `json:"stop_events,omitempty"`
	EventKinds               []string       `json:"event_kinds,omitempty"`
	ProviderDiagnostics      map[string]any `json:"provider_diagnostics,omitempty"`
	// AmbiguousSources names each evidence class that matched more than one
	// file. The observation is then omitted rather than taken from an arbitrary
	// attempt, so a recorded phase or summary always describes a unique source.
	AmbiguousSources []string `json:"ambiguous_sources,omitempty"`
}

// matrixSolePath returns the only path in items. Zero or several yield "", with
// several also recorded in ambiguous, because silently picking the first would
// make the diagnostic describe an arbitrary attempt instead of the one that ran.
func matrixSolePath(items []string, class string, ambiguous *[]string) string {
	switch len(items) {
	case 1:
		return items[0]
	case 0:
		return ""
	default:
		*ambiguous = append(*ambiguous, class)
		return ""
	}
}

type matrixRecord struct {
	Name          string             `json:"name"`
	Provider      string             `json:"provider"`
	Binary        string             `json:"binary,omitempty"`
	BinarySHA256  string             `json:"binary_sha256,omitempty"`
	RawVersion    string             `json:"raw_version,omitempty"`
	Version       matrixVersionView  `json:"version_admission"`
	OK            bool               `json:"ok"`
	Result        string             `json:"result"`
	FailureStage  string             `json:"failure_stage,omitempty"`
	Failure       string             `json:"failure,omitempty"`
	Guard         *matrixGuard       `json:"guard,omitempty"`
	MarkerSHA256  string             `json:"marker_sha256,omitempty"`
	StateDir      string             `json:"state_dir,omitempty"`
	Status        *cliTaskStatus     `json:"status,omitempty"`
	SelectedEvent *matrixEvent       `json:"selected_event,omitempty"`
	Artifact      *matrixArtifact    `json:"result_artifact,omitempty"`
	FinalStatus   *cliTaskStatus     `json:"final_status,omitempty"`
	Ack           string             `json:"ack,omitempty"`
	Accept        string             `json:"accept,omitempty"`
	HostLog       string             `json:"host_log_excerpt,omitempty"`
	Spool         []matrixSpoolEntry `json:"spool_inventory,omitempty"`
	Diagnostics   *matrixDiagnostics `json:"diagnostics,omitempty"`
	Cleanup       string             `json:"cleanup,omitempty"`
	PreservedDir  string             `json:"preserved_failure_scene,omitempty"`
	StartedAt     string             `json:"started_at"`
	FinishedAt    string             `json:"finished_at,omitempty"`
	DurationMS    int64              `json:"duration_ms,omitempty"`
}

type matrixToolchain struct {
	GoVersion  string `json:"go_version"`
	Goos       string `json:"goos"`
	Goarch     string `json:"goarch"`
	GitHead    string `json:"git_head,omitempty"`
	GitDirty   bool   `json:"git_dirty"`
	RecordedAt string `json:"recorded_at"`
}

// matrixEntryPoint states the evidence tier explicitly. A source-tree build
// needs ORCHESTRATOR_ENABLE_TEST_FAKE to get past install.PinRunningVersion, so
// its result is not a packaged-entry pass and must never be recorded as one.
type matrixEntryPoint struct {
	Kind               string `json:"kind"`
	OrchestratorBinary string `json:"orchestrator_binary"`
	OrchestratorSHA256 string `json:"orchestrator_sha256"`
	PackagePinBypassed bool   `json:"package_pin_bypassed"`
	FormalPackageEntry bool   `json:"formal_package_entry"`
	InstallDestination string `json:"install_destination,omitempty"`
	InstallVersion     string `json:"install_version,omitempty"`
	DoctorHealthy      *bool  `json:"doctor_healthy,omitempty"`
	Note               string `json:"note"`
}

type matrixSummary struct {
	Total             int      `json:"total"`
	RealAccepted      int      `json:"real_accepted"`
	GuardBlocked      int      `json:"guard_blocked"`
	BinaryMissing     int      `json:"binary_unavailable"`
	Failed            int      `json:"failed"`
	IncompleteTrials  []string `json:"incomplete_trial_items"`
	UnsettledCleanups []string `json:"unsettled_cleanups,omitempty"`
}

// matrixIsDurableSpoolEvent reports whether an event file sits in the Host's own
// per-segment spool, which is <state>/host-spool/<launch>/<attempt>/<segment>/.
// Anything under .reports or scratch is provider-writable, so a file there shaped
// like an event is not this run's evidence no matter how well it parses.
func matrixIsDurableSpoolEvent(path string) bool {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for _, part := range parts {
		if part == ".reports" || part == "scratch" {
			return false
		}
	}
	for index, part := range parts {
		if part == "host-spool" {
			return len(parts)-index == 5 && strings.HasPrefix(parts[len(parts)-1], "event-")
		}
	}
	return false
}

type matrixRound struct {
	Version    int              `json:"version"`
	Round      string           `json:"round"`
	Repo       string           `json:"repo"`
	Selected   string           `json:"selected_case,omitempty"`
	Toolchain  matrixToolchain  `json:"toolchain"`
	EntryPoint matrixEntryPoint `json:"entry_point"`
	Summary    matrixSummary    `json:"summary"`
	Cases      []*matrixRecord  `json:"cases"`
}

// matrixRunner carries the entry point under test and whether the package-pin
// bypass had to be enabled to reach submit at all.
type matrixRunner struct {
	binary     string
	enableFake bool
	entry      matrixEntryPoint
}

func (r matrixRunner) cmd(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, r.binary, args...)
	// AGY and Grok auto-updaters are disabled so a self-update cannot change a
	// pinned executable mid-run.
	env := append(os.Environ(), "AGY_CLI_DISABLE_AUTO_UPDATE=true", "GROK_DISABLE_AUTOUPDATER=1")
	if r.enableFake {
		env = append(env, "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	}
	command.Env = env
	return command
}

// matrixSelection turns ORCHESTRATOR_REAL_CASE into a membership test. An empty
// specification selects every case; otherwise it accepts a comma-separated list
// so several endpoints can share one serially executed round.
func matrixSelection(spec string) func(string) bool {
	if strings.TrimSpace(spec) == "" {
		return func(string) bool { return true }
	}
	want := make(map[string]bool)
	for _, name := range strings.Split(spec, ",") {
		if name = strings.TrimSpace(name); name != "" {
			want[name] = true
		}
	}
	return func(name string) bool { return want[name] }
}

func TestRealVersionMatrixRead(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_REAL_VERSION_MATRIX") != "1" {
		t.Skip("requires ORCHESTRATOR_REAL_VERSION_MATRIX=1; runs real provider CLIs and consumes model quota")
	}
	repo := matrixRepoRoot(t)
	only := os.Getenv("ORCHESTRATOR_REAL_CASE")
	selected := matrixSelection(only)
	runner := matrixEntry(t, repo)

	round := &matrixRound{
		Version:    matrixSchemaVer,
		Round:      time.Now().UTC().Format("20060102T150405Z"),
		Repo:       repo,
		Selected:   only,
		EntryPoint: runner.entry,
		Toolchain:  matrixToolchainRecord(repo),
	}
	if round.EntryPoint.FormalPackageEntry {
		healthy, detail := matrixDoctor(t, runner)
		round.EntryPoint.DoctorHealthy = &healthy
		if !healthy {
			t.Fatalf("installed entry point is not healthy, refusing to record a packaged pass: %s", detail)
		}
	}
	for _, tc := range matrixCases() {
		if !selected(tc.name) {
			continue
		}
		round.Cases = append(round.Cases, runMatrixCase(t, repo, runner, tc))
	}
	if len(round.Cases) == 0 {
		t.Fatalf("ORCHESTRATOR_REAL_CASE=%q matched no case", only)
	}
	round.Summary = matrixSummarize(round.Cases)

	evidenceDir := filepath.Join(repo, matrixEvidenceRel, round.Round)
	// Mkdir, not MkdirAll: a round name is a second-resolution UTC stamp, and
	// reusing a directory would silently replace the evidence of an earlier round.
	if err := os.Mkdir(evidenceDir, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			t.Fatalf("refusing to overwrite existing round evidence: %s", evidenceDir)
		}
		t.Fatalf("evidence directory: %v", err)
	}
	body, err := json.MarshalIndent(round, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(evidenceDir, "round.json")
	if err := os.WriteFile(evidencePath, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s (entry point: %s, package pin bypassed: %t)", evidencePath, round.EntryPoint.Kind, round.EntryPoint.PackagePinBypassed)

	for _, record := range round.Cases {
		if record.OK {
			continue
		}
		t.Errorf("%s: result=%s stage=%s failure=%s", record.Name, record.Result, record.FailureStage, record.Failure)
	}
	if len(round.Summary.IncompleteTrials) > 0 {
		t.Errorf("matrix incomplete: %d of %d cases are not real accepted trials: %s",
			len(round.Summary.IncompleteTrials), round.Summary.Total, strings.Join(round.Summary.IncompleteTrials, "; "))
	}
	if len(round.Summary.UnsettledCleanups) > 0 {
		t.Errorf("%d of %d cases could not prove their process set was gone, and their scenes were left in place: %s",
			len(round.Summary.UnsettledCleanups), round.Summary.Total, strings.Join(round.Summary.UnsettledCleanups, "; "))
	}
}

// TestRealVersionMatrixInvocationStages is opt-in and isolated. It asserts the
// prelaunch stages in-process so a launch failure can be attributed to a stage
// instead of the collapsed "launch_failed" cause code. It never submits a task,
// never starts a coordinator, and never calls a model.
func TestRealVersionMatrixInvocationStages(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_REAL_VERSION_MATRIX") != "1" || os.Getenv("ORCHESTRATOR_REAL_DIAGNOSTIC") != "1" {
		t.Skip("requires ORCHESTRATOR_REAL_VERSION_MATRIX=1 and ORCHESTRATOR_REAL_DIAGNOSTIC=1")
	}
	repo := matrixRepoRoot(t)
	selected := matrixSelection(os.Getenv("ORCHESTRATOR_REAL_CASE"))
	for _, tc := range matrixCases() {
		if !selected(tc.name) {
			continue
		}
		binary, unavailable := resolveCaseBinary(repo, tc)
		if unavailable != "" {
			t.Errorf("%s: stage resolve_binary: %s", tc.name, unavailable)
			continue
		}
		root := matrixTempRoot(t, "verstage-")
		t.Cleanup(func() { _ = os.RemoveAll(root) })

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		lock, view, err := matrixPin(ctx, tc.provider, binary)
		if err != nil {
			cancel()
			t.Errorf("%s: stage pin: %v", tc.name, err)
			continue
		}
		if !view.Admitted {
			cancel()
			t.Errorf("%s: stage version_admission: %s (raw %q)", tc.name, view.Reason, lock.Binary.Version)
			continue
		}
		profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 30000}
		if tc.grokWrite {
			profile.GrokSessionWrite = true
		}
		body, marshalErr := json.Marshal(invocationPayload{Provider: string(tc.provider), ProviderLock: &lock, Profile: &profile, Directory: root, Prompt: "stage diagnostic"})
		if marshalErr != nil {
			cancel()
			t.Errorf("%s: stage payload: %v", tc.name, marshalErr)
			continue
		}
		inv, err := invocationForGrant(ctx, contract.LaunchCommand{
			RunID: matrixRunID, TaskID: matrixTaskID, AttemptID: "attempt", SegmentID: "segment",
			CommandID: "command", ReservationID: "reservation", WorkRevision: matrixWorkRev,
			GrantedActiveMS: 30000, AdapterPayload: body,
		})
		cancel()
		if err != nil {
			// Codex is expected to stop at the trial guard. That is a blocked
			// trial, not a passing stage, so it is reported and never asserted ok.
			var blocked *adapter.TrialBlockedError
			if errors.As(err, &blocked) && blocked != nil && tc.provider == adapter.ProviderCodex {
				t.Logf("%s: stage invocation blocked at the Codex trial guard, missing=%v", tc.name, blocked.Missing)
				continue
			}
			t.Errorf("%s: stage invocation: %v", tc.name, err)
			continue
		}
		if inv == nil || len(inv.Args()) == 0 || inv.Args()[0] != binary {
			t.Errorf("%s: stage invocation produced an unusable argv", tc.name)
			continue
		}
		// sandboxCommand derives its rules from an EvalSymlinks'd scratch path,
		// while the report capability path is built from the spool root exactly as
		// handed over. Both must share one canonical prefix or the launch is
		// rejected with report_capability_path_untrusted, which the launch ledger
		// records only as the opaque "launch_failed".
		if canonical, evalErr := filepath.EvalSymlinks(root); evalErr != nil || canonical != root {
			t.Errorf("%s: stage sandbox_prefix: %s is not symlink-canonical (%v)", tc.name, root, evalErr)
			continue
		}
		if inv.WorkingDirectory() != root {
			t.Errorf("%s: stage working_directory: %s != %s", tc.name, inv.WorkingDirectory(), root)
			continue
		}
		t.Logf("%s: stages resolve/pin/version/invocation/sandbox-prefix ok (normalized %s)", tc.name, view.Normalized)
	}
}

// matrixEntry resolves the orchestrator entry point for this round.
//
// ORCHESTRATOR_MATRIX_BINARY selects an already packaged and installed binary
// (<destination>/versions/<semver>/bin/codex-orchestrator). That exercises
// install.PinRunningVersion for real, so no bypass flag is set and the round is
// a formal packaged-entry result. Without it the harness falls back to a
// source-tree build, which needs ORCHESTRATOR_ENABLE_TEST_FAKE=1 to get past
// the package pin; that tier is recorded and is not a packaged-entry pass.
func matrixEntry(t *testing.T, repo string) matrixRunner {
	t.Helper()
	if external := os.Getenv("ORCHESTRATOR_MATRIX_BINARY"); external != "" {
		binary, err := filepath.EvalSymlinks(external)
		if err != nil {
			t.Fatalf("ORCHESTRATOR_MATRIX_BINARY unusable: %v", err)
		}
		body, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		entry := matrixEntryPoint{
			Kind:               "external_build",
			OrchestratorBinary: binary,
			OrchestratorSHA256: matrixSHA256Hex(body),
			PackagePinBypassed: false,
			Note:               "externally supplied binary; install.PinRunningVersion is exercised and no bypass flag is set",
		}
		// install.runningPackageIdentity requires exactly this layout.
		versionRoot := filepath.Dir(filepath.Dir(binary))
		if filepath.Base(binary) == "codex-orchestrator" && filepath.Base(filepath.Dir(binary)) == "bin" &&
			filepath.Base(filepath.Dir(versionRoot)) == "versions" {
			entry.Kind = "installed_package"
			entry.FormalPackageEntry = true
			entry.InstallVersion = filepath.Base(versionRoot)
			entry.InstallDestination = filepath.Dir(filepath.Dir(versionRoot))
			entry.Note = "packaged and installed entry point; install.PinRunningVersion is exercised and no bypass flag is set"
		}
		return matrixRunner{binary: binary, enableFake: false, entry: entry}
	}

	buildRoot := matrixTempRoot(t, "vermat-build-")
	t.Cleanup(func() { _ = os.RemoveAll(buildRoot) })
	bin := filepath.Join(buildRoot, "orchestrator")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(repo, "tools", "orchestrator", "cmd", "orchestrator")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build orchestrator: %v\n%s", err, output)
	}
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return matrixRunner{
		binary:     bin,
		enableFake: true,
		entry: matrixEntryPoint{
			Kind:               "source_tree_build",
			OrchestratorBinary: bin,
			OrchestratorSHA256: matrixSHA256Hex(body),
			PackagePinBypassed: true,
			FormalPackageEntry: false,
			Note: "ORCHESTRATOR_ENABLE_TEST_FAKE=1 bypasses install.PinRunningVersion (running_package_invalid). " +
				"This is a source-tree build, not the packaged/installed entry point; the result must not be " +
				"recorded as a formal packaged-entry pass.",
		},
	}
}

// matrixDoctor asks the installed tree to verify itself. It is stricter than
// the pin path: it re-hashes every installed file and the pinned interpreter.
func matrixDoctor(t *testing.T, runner matrixRunner) (bool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	request := filepath.Join(filepath.Dir(runner.entry.InstallDestination), "matrix-doctor.json")
	body, _ := json.Marshal(map[string]any{"destination_root": runner.entry.InstallDestination})
	if err := os.WriteFile(request, body, 0600); err != nil {
		return false, "write doctor request: " + err.Error()
	}
	defer func() { _ = os.Remove(request) }()
	output, err := runner.cmd(ctx, "doctor", "--request", request).CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("doctor: %v: %s", err, matrixExcerpt(redactSecrets(string(output))))
	}
	var report struct {
		Status string `json:"status"`
		Result struct {
			Healthy bool     `json:"healthy"`
			Issues  []string `json:"issues"`
		} `json:"result"`
	}
	if jsonErr := json.Unmarshal(output, &report); jsonErr != nil {
		return false, "doctor response: " + jsonErr.Error()
	}
	if report.Status != "ok" || !report.Result.Healthy {
		return false, fmt.Sprintf("status=%s issues=%v", report.Status, report.Result.Issues)
	}
	return true, "healthy"
}

func runMatrixCase(t *testing.T, repo string, runner matrixRunner, tc matrixCase) *matrixRecord {
	t.Helper()
	started := time.Now()
	record := &matrixRecord{
		Name:      tc.name,
		Provider:  string(tc.provider),
		Result:    "not_run",
		StartedAt: started.UTC().Format(time.RFC3339Nano),
	}
	closeOut := func(stage, result string) *matrixRecord {
		record.FailureStage = stage
		record.Result = result
		record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.DurationMS = time.Since(started).Milliseconds()
		return record
	}

	binary, unavailable := resolveCaseBinary(repo, tc)
	record.Binary = binary
	if unavailable != "" {
		record.Failure = unavailable
		return closeOut("resolve_binary", "binary_unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	probeCtx, cancelProbe := context.WithTimeout(ctx, 40*time.Second)
	lock, view, err := matrixPin(probeCtx, tc.provider, binary)
	cancelProbe()
	record.Version = view
	if err != nil {
		record.Failure = "provider probe: " + err.Error()
		return closeOut("pin", "pin_failed")
	}
	record.BinarySHA256 = lock.Binary.SHA256
	record.RawVersion = lock.Binary.Version
	if !view.Admitted {
		record.Failure = "version not admitted: " + view.Reason
		return closeOut("version_admission", "version_rejected")
	}
	// Ask the authoritative production gate directly. For Codex this is the trial
	// guard, which fires inside the Host where the launch ledger collapses the
	// cause into the opaque "launch_failed". Naming the real blocker and its
	// missing contracts here keeps a blocked trial from being misread as a
	// generic task failure.
	if gateErr := adapter.LaunchAdmission(tc.provider, lock.Binary.Version, lock.Binary.SHA256); gateErr != nil {
		var blocked *adapter.TrialBlockedError
		if errors.As(gateErr, &blocked) && blocked != nil {
			record.Guard = &matrixGuard{Code: blocked.Error(), Missing: blocked.Missing, TrialComplete: false}
		}
	}

	root := matrixTempRoot(t, "vermat-")
	state := filepath.Join(root, "state")
	work := filepath.Join(root, "work")
	for _, dir := range []string{state, work} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			record.Failure = "isolate directories: " + err.Error()
			_ = os.RemoveAll(root)
			record.Cleanup = "removed"
			return closeOut("isolate", "harness_failed")
		}
	}
	record.StateDir = state

	// Shutdown and evidence retention are decided only after the coordinator is
	// provably gone. A state directory is never deleted while its coordinator may
	// still hold the socket, and a failure scene is never renamed out from under
	// a running process.
	var coord *matrixCoordinator
	defer func() { matrixCloseOutCleanup(coord, root, repo, record, started) }()
	defer func() {
		if record.Diagnostics == nil {
			record.Diagnostics = matrixDiagnose(state)
		}
	}()

	marker, err := matrixMarker()
	if err != nil {
		record.Failure = "marker: " + err.Error()
		return closeOut("marker", "harness_failed")
	}
	record.MarkerSHA256 = matrixSHA256Hex([]byte(marker))
	if err = os.WriteFile(filepath.Join(work, matrixMarkerFile), []byte(marker+"\n"), 0600); err != nil {
		record.Failure = "write marker: " + err.Error()
		return closeOut("marker", "harness_failed")
	}

	prompt := "Read " + matrixMarkerFile + " in the working directory with your file-reading tool. Reply with only the exact file contents."
	submitCtx, cancelSubmit := context.WithTimeout(ctx, 60*time.Second)
	submitted, controlFile, err := submitMatrix(submitCtx, runner, root, state, work, prompt, lock, tc)
	cancelSubmit()
	coord = &matrixCoordinator{state: state, root: root, expectedCommand: "serve --state-dir " + state}
	if pid, _, pidErr := matrixCoordinatorPID(state); pidErr == nil {
		coord.pid = pid
		// The reference birth has to be taken here, before any other process can
		// occupy the pid. Failing to read it now leaves birth empty, which
		// stopVerified reads as "cannot prove ownership, refuse to signal".
		if birth, birthErr := process.Birth(pid); birthErr == nil {
			coord.birth = birth
		}
	}
	record.HostLog = matrixExcerpt(redactSecrets(string(readMaybe(filepath.Join(state, "source-host.log")))))
	if err != nil {
		record.Failure = err.Error()
		record.Spool = matrixSpoolInventory(state)
		record.Diagnostics = matrixDiagnose(state)
		if guard := matrixGuardFrom(submitted); guard != nil {
			// A guard that stops the launch before it starts is an unfinished
			// trial item, never a pass.
			record.Guard = guard
			return closeOut("submit", "blocked_before_launch")
		}
		return closeOut("submit", "submit_failed")
	}

	statusCtx, cancelStatus := context.WithTimeout(ctx, 200*time.Second)
	defer cancelStatus()
	deadline := time.Now().Add(170 * time.Second)
	var status cliTaskStatus
	for {
		status = quietStatus(statusCtx, runner, state, matrixTaskID, controlFile)
		if status.Status == "result_ready" || status.Status == "completed" || status.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-statusCtx.Done():
			record.Failure = "status polling: " + statusCtx.Err().Error()
			return closeOut("status", "harness_failed")
		case <-time.After(500 * time.Millisecond):
		}
	}
	record.Status = &status
	record.HostLog = matrixExcerpt(redactSecrets(string(readMaybe(filepath.Join(state, "source-host.log")))))
	if status.Status != "result_ready" && status.Status != "completed" {
		record.Failure = "terminal status " + status.Status
		record.Spool = matrixSpoolInventory(state)
		record.Diagnostics = matrixDiagnose(state)
		if record.Guard != nil {
			// A guard-blocked case is an unfinished trial item, never a pass. The
			// classification is only trustworthy if the worker really never got as
			// far as preparing or spawning a process.
			if record.Diagnostics.PreparedEventPresent || record.Diagnostics.SpawnedEventPresent {
				record.Failure += "; trial guard was recorded but the worker reached launch, so the blocked classification is withheld"
				return closeOut("terminal_status", "task_failed")
			}
			record.Failure = "trial gate blocked the launch before any process was prepared or spawned: " + record.Guard.Code
			return closeOut("launch_gate", "blocked_before_launch")
		}
		return closeOut("terminal_status", "task_failed")
	}

	collectCtx, cancelCollect := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCollect()
	delivery, event, collectOut, err := matrixCollectResult(collectCtx, runner, state, controlFile, status.SegmentID)
	if err != nil {
		record.Failure = err.Error()
		record.Ack = matrixExcerpt(redactSecrets(string(collectOut)))
		record.Spool = matrixSpoolInventory(state)
		record.Diagnostics = matrixDiagnose(state)
		return closeOut("collect", "collect_failed")
	}
	record.SelectedEvent = &matrixEvent{
		EventID: event.EventID, EventRevision: event.EventRevision, PayloadHash: event.PayloadHash,
		ActionSlot: event.ActionSlot, AttemptID: event.AttemptID, SegmentID: event.SegmentID,
		Kind: event.Kind, Sequence: event.Sequence,
	}

	// Verify the registered final artifact before any ack or accept. A marker
	// found somewhere in the spool proves nothing about the delivered answer.
	artifact, err := matrixVerifyArtifact(event.Artifact, marker)
	record.Artifact = artifact
	if err != nil {
		record.Failure = err.Error()
		record.Spool = matrixSpoolInventory(state)
		return closeOut("artifact", "artifact_unverified")
	}

	ackOut, acceptOut, err := matrixAckAccept(collectCtx, runner, state, root, controlFile, delivery, event)
	record.Ack = matrixExcerpt(redactSecrets(string(ackOut)))
	record.Accept = matrixExcerpt(redactSecrets(string(acceptOut)))
	if err != nil {
		record.Failure = err.Error()
		record.Spool = matrixSpoolInventory(state)
		return closeOut("accept", "owner_loop_failed")
	}

	final := quietStatus(collectCtx, runner, state, matrixTaskID, controlFile)
	record.FinalStatus = &final
	if final.Status != "completed" {
		record.Failure = "final persistent status " + final.Status + ", expected completed"
		return closeOut("final_status", "acceptance_incomplete")
	}
	record.OK = true
	// Preserve the same bounded protocol counters for accepted trials as for
	// failures, before the private successful scene is removed.
	record.Diagnostics = matrixDiagnose(state)
	return closeOut("", "read_collected_accepted")
}

func matrixSummarize(cases []*matrixRecord) matrixSummary {
	summary := matrixSummary{Total: len(cases)}
	for _, record := range cases {
		// The trial verdict and the cleanup verdict are separate facts. A round whose
		// process set was never accounted for has to be named even when the owner
		// loop itself finished, because what it left behind is the evidence.
		if outcome := matrixCleanupOutcome(record.Cleanup); outcome != "" && !outcome.settled() {
			summary.UnsettledCleanups = append(summary.UnsettledCleanups, record.Name+": "+record.Cleanup)
		}
		switch {
		case record.OK:
			summary.RealAccepted++
		case record.Result == "blocked_before_launch":
			summary.GuardBlocked++
			detail := record.Name + ": " + record.Result
			if record.Guard != nil {
				detail += " (" + record.Guard.Code + ": " + strings.Join(record.Guard.Missing, ", ") + ")"
			}
			summary.IncompleteTrials = append(summary.IncompleteTrials, detail)
		case record.Result == "binary_unavailable":
			summary.BinaryMissing++
			summary.IncompleteTrials = append(summary.IncompleteTrials, record.Name+": "+record.Failure)
		default:
			summary.Failed++
			summary.IncompleteTrials = append(summary.IncompleteTrials, record.Name+": "+record.Result+" at "+record.FailureStage)
		}
	}
	return summary
}

// resolveCaseBinary returns a symlink-canonical absolute path. adapter.Probe
// and adapter.VerifyExecutable both require EvalSymlinks(path) == path.
func resolveCaseBinary(repo string, tc matrixCase) (string, string) {
	if override := os.Getenv(tc.envOverride); override != "" {
		resolved, err := filepath.EvalSymlinks(override)
		if err != nil {
			return "", fmt.Sprintf("%s unusable: %v", tc.envOverride, err)
		}
		return resolved, ""
	}
	if tc.relBinary == "" {
		return "", "binary is not pinned inside the repository; set " + tc.envOverride + " to an absolute path"
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(repo, tc.relBinary))
	if err != nil {
		return "", "pinned binary missing: " + tc.relBinary
	}
	return resolved, ""
}

func matrixPin(ctx context.Context, provider adapter.Provider, path string) (adapter.ProviderLock, matrixVersionView, error) {
	lock, err := adapter.Probe(ctx, provider, path)
	if err != nil {
		return adapter.ProviderLock{}, matrixVersionView{}, err
	}
	view := adapter.DescribeVersion(provider, lock.Binary.Version)
	return lock, matrixVersionView{
		Admitted: view.Admitted, Normalized: view.Normalized, Reason: view.Reason,
		Baseline: view.Baseline, Ceiling: view.Ceiling, FrozenAt: view.FrozenAt, Protocol: view.Protocol,
	}, nil
}

func submitMatrix(ctx context.Context, runner matrixRunner, root, state, work, prompt string, lock adapter.ProviderLock, tc matrixCase) ([]byte, string, error) {
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		return nil, "", err
	}
	// The request file stays outside the model's working directory so the read
	// task sees only its marker file.
	requestPath := filepath.Join(root, "request.json")
	write := func(value any) error {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(requestPath, body, 0600)
	}
	if err = write(map[string]any{"controller_thread": matrixRunID, "origin_pid": os.Getpid(), "origin_birth": birth}); err != nil {
		return nil, "", err
	}
	output, err := runner.cmd(ctx, "owner-bind", "--state-dir", state, "--request", requestPath).CombinedOutput()
	if err != nil {
		return output, "", fmt.Errorf("owner-bind: %w: %s", err, matrixExcerpt(redactSecrets(string(output))))
	}
	var owner map[string]any
	if err = json.Unmarshal(output, &owner); err != nil {
		return output, "", fmt.Errorf("owner-bind response: %w", err)
	}
	// owner-bind projects owner_mode=local plus a real owner_capability path, so
	// submit verifies the local projection instead of needing a bypass.
	delete(owner, "version")
	owner["run_id"] = matrixRunID
	owner["plan_revision"] = matrixWorkRev
	owner["delivery_mode"] = "collect"
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: matrixMaxActiveMS}
	if err = matrixConfigureModel(&profile); err != nil {
		return nil, "", err
	}
	if tc.grokWrite {
		profile.GrokSessionWrite = true
	}
	owner["tasks"] = []any{map[string]any{
		"id": matrixTaskID, "max_attempts": 1, "max_active_ms": matrixMaxActiveMS,
		"completion_policy": "owner_review",
		"adapter": map[string]any{
			"provider": string(tc.provider), "provider_lock": lock, "profile": profile,
			"directory": work, "prompt": prompt,
		},
	}}
	if err = write(owner); err != nil {
		return nil, "", err
	}
	output, err = runner.cmd(ctx, "submit", "--state-dir", state, "--request", requestPath).CombinedOutput()
	if err != nil {
		return output, "", fmt.Errorf("submit: %w: %s", err, matrixExcerpt(redactSecrets(string(output))))
	}
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if jsonErr := json.Unmarshal(output, &submitted); jsonErr != nil || submitted.ControlFile == "" {
		return output, "", fmt.Errorf("submit response missing control file: %s", matrixExcerpt(redactSecrets(string(output))))
	}
	return output, submitted.ControlFile, nil
}

type matrixDelivery struct {
	ID    string
	Proof string
}

// matrixCollectResult selects the result event that this task, work revision and
// segment actually registered, instead of taking the first collected event.
func matrixCollectResult(ctx context.Context, runner matrixRunner, state, control, segmentID string) (matrixDelivery, contract.Event, []byte, error) {
	output, err := runner.cmd(ctx, "collect", "--state-dir", state, "--task-id", matrixTaskID, "--control-file", control).CombinedOutput()
	if err != nil {
		return matrixDelivery{}, contract.Event{}, output, fmt.Errorf("collect: %w: %s", err, matrixExcerpt(redactSecrets(string(output))))
	}
	var page coordinator.CollectResponse
	if jsonErr := json.Unmarshal(output, &page); jsonErr != nil {
		return matrixDelivery{}, contract.Event{}, output, fmt.Errorf("collect response: %w", jsonErr)
	}
	if page.DeliveryID == "" || page.CollectionProofSHA256 == "" {
		return matrixDelivery{}, contract.Event{}, output, errors.New("collect receipt missing delivery id or collection proof")
	}
	var matches []contract.Event
	for _, event := range page.Events {
		if event.RunID != matrixRunID || event.TaskID != matrixTaskID || event.Kind != contract.EventResult {
			continue
		}
		if event.WorkRevision != matrixWorkRev || event.Artifact == nil || event.EventID == "" {
			continue
		}
		if segmentID != "" && event.SegmentID != segmentID {
			continue
		}
		matches = append(matches, event)
	}
	if len(matches) == 0 {
		return matrixDelivery{}, contract.Event{}, output, fmt.Errorf("no acceptable result event for task %s revision %d segment %q among %d collected events",
			matrixTaskID, matrixWorkRev, segmentID, len(page.Events))
	}
	if len(matches) > 1 {
		return matrixDelivery{}, contract.Event{}, output, fmt.Errorf("ambiguous result events: %d candidates for one attempt/segment", len(matches))
	}
	return matrixDelivery{ID: page.DeliveryID, Proof: page.CollectionProofSHA256}, matches[0], output, nil
}

func matrixVerifyArtifact(ref *contract.ArtifactRef, marker string) (*matrixArtifact, error) {
	if ref == nil || ref.Path == "" {
		return nil, errors.New("result event carries no artifact reference")
	}
	incomplete := &matrixArtifact{Path: ref.Path, DeclaredSize: ref.Size, DeclaredSHA256: ref.SHA256}
	// Read the registered file itself, not whatever the reference points at. The
	// Host writes the spool artifact with O_NOFOLLOW, and the harness's own marker
	// file already holds the exact accepted bytes, so a redirected reference would
	// otherwise pass the digest, the size and the content rule together.
	info, err := os.Lstat(ref.Path)
	if err != nil {
		return incomplete, fmt.Errorf("read result artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return incomplete, fmt.Errorf("result artifact %s is not a regular file (mode %s)", ref.Path, info.Mode())
	}
	file, err := os.OpenFile(ref.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return incomplete, fmt.Errorf("read result artifact: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, matrixArtifactCap+1))
	if err != nil {
		return incomplete, fmt.Errorf("read result artifact: %w", err)
	}
	if len(body) > matrixArtifactCap {
		return incomplete, fmt.Errorf("result artifact %s exceeds the %d byte spool cap", ref.Path, matrixArtifactCap)
	}
	observed := matrixSHA256Hex(body)
	exact := string(body) == marker || string(body) == marker+"\n"
	artifact := &matrixArtifact{
		Path: ref.Path, DeclaredSize: ref.Size, DeclaredSHA256: ref.SHA256,
		ObservedSize: int64(len(body)), ObservedSHA256: observed,
		SizeMatches:   int64(len(body)) == ref.Size,
		DigestMatches: strings.EqualFold(observed, ref.SHA256),
		ExactMatch:    exact,
	}
	switch {
	case !artifact.DigestMatches:
		return artifact, fmt.Errorf("artifact digest mismatch: declared %s observed %s", ref.SHA256, observed)
	case !artifact.SizeMatches:
		return artifact, fmt.Errorf("artifact size mismatch: declared %d observed %d", ref.Size, len(body))
	case !artifact.ExactMatch && strings.Contains(string(body), marker):
		// The marker file is written as marker+"\n" and the prompt asks for the
		// file contents verbatim, so exactly one trailing newline is the only
		// tolerated difference. Anything around the marker is a different answer,
		// including a previous round's marker and an error body.
		return artifact, fmt.Errorf("final answer wraps this round's marker in other content (%d bytes, marker plus one newline is %d)", len(body), len(marker)+1)
	case !artifact.ExactMatch:
		return artifact, fmt.Errorf("final answer is not this round's marker (%d bytes, marker absent)", len(body))
	}
	return artifact, nil
}

func matrixAckAccept(ctx context.Context, runner matrixRunner, state, root, control string, delivery matrixDelivery, event contract.Event) ([]byte, []byte, error) {
	requestPath := filepath.Join(root, "ack-request.json")
	body, err := json.Marshal(map[string]any{
		"version": 1, "task_id": matrixTaskID, "control_file": control,
		"delivery_id": delivery.ID, "collection_proof_sha256": delivery.Proof,
		"decisions": []map[string]any{{
			"event_id": event.EventID, "event_revision": event.EventRevision,
			"event_hash": event.PayloadHash, "action_slot": event.ActionSlot,
			"decision": "handled", "command_id": "ack-" + matrixTaskID,
		}},
	})
	if err != nil {
		return nil, nil, err
	}
	if err = os.WriteFile(requestPath, body, 0600); err != nil {
		return nil, nil, err
	}
	ackOut, err := runner.cmd(ctx, "ack", "--state-dir", state, "--request", requestPath).CombinedOutput()
	if err != nil {
		return ackOut, nil, fmt.Errorf("ack: %w: %s", err, matrixExcerpt(redactSecrets(string(ackOut))))
	}
	acceptOut, err := runner.cmd(ctx, "accept",
		"--state-dir", state, "--task-id", matrixTaskID, "--control-file", control,
		"--work-revision", strconv.Itoa(matrixWorkRev),
		"--event-id", event.EventID,
		"--event-revision", strconv.FormatInt(event.EventRevision, 10),
		"--event-hash", event.PayloadHash,
		"--action-slot", event.ActionSlot,
		"--decision", "accept",
		"--command-id", "accept-"+matrixTaskID,
	).CombinedOutput()
	if err != nil {
		return ackOut, acceptOut, fmt.Errorf("accept: %w: %s", err, matrixExcerpt(redactSecrets(string(acceptOut))))
	}
	return ackOut, acceptOut, nil
}

// matrixStopOutcome names what the harness can actually prove about the process
// set it started. Only a settled outcome may free the state directory.
type matrixStopOutcome string

const (
	stopNoCoordinator      matrixStopOutcome = "no_coordinator"
	stopNotStarted         matrixStopOutcome = "not_started"
	stopExited             matrixStopOutcome = "exited"
	stopExitedBeforeSignal matrixStopOutcome = "exited_before_signal"
	stopIdentityUnknown    matrixStopOutcome = "identity_unknown"
	stopProbeFailed        matrixStopOutcome = "probe_failed"
	stopPIDRecycled        matrixStopOutcome = "pid_recycled"
	stopPIDFileUnreadable  matrixStopOutcome = "pid_file_unreadable"
	stopPIDFileMismatch    matrixStopOutcome = "pid_file_mismatch"
	stopHolderSurvives     matrixStopOutcome = "holder_survives"
	stopSignalFailed       matrixStopOutcome = "signal_failed"
	stopExitUnverified     matrixStopOutcome = "exit_unverified"
	stopExitTimeout        matrixStopOutcome = "exit_timeout"
)

// settled reports whether the outcome proves that no process of ours can still
// hold the state directory. Everything else leaves the scene exactly where it is:
// no signal, and neither a delete nor a rename.
func (o matrixStopOutcome) settled() bool {
	return o == stopNoCoordinator || o == stopNotStarted || o == stopExited || o == stopExitedBeforeSignal
}

// matrixCleanupOutcome reads the outcome back out of the recorded cleanup string,
// which is the outcome optionally followed by a reason and a disposition.
func matrixCleanupOutcome(cleanup string) matrixStopOutcome {
	cut := len(cleanup)
	for _, separator := range []string{":", ";"} {
		if at := strings.Index(cleanup, separator); at >= 0 && at < cut {
			cut = at
		}
	}
	return matrixStopOutcome(strings.TrimSpace(cleanup[:cut]))
}

type matrixCleanupAction string

const (
	cleanupRemoveState matrixCleanupAction = "remove_state"
	cleanupMoveScene   matrixCleanupAction = "move_failure_scene"
	cleanupKeepInPlace matrixCleanupAction = "keep_in_place"
)

func matrixCleanupPlan(outcome matrixStopOutcome, accepted bool) matrixCleanupAction {
	if !outcome.settled() {
		return cleanupKeepInPlace
	}
	if accepted {
		return cleanupRemoveState
	}
	return cleanupMoveScene
}

// matrixProcessProbe is the only place the harness asks the kernel about a
// process. Real rounds use matrixKernelProbe; the default-automation regressions
// inject a scripted probe so every identity outcome can be asserted without a
// provider and without racing a live pid.
type matrixProcessProbe interface {
	Birth(pid int) (string, error)
	CommandLine(pid int) (string, error)
	Signal(pid int, sig syscall.Signal) error
}

type matrixKernelProbe struct{}

func (matrixKernelProbe) Birth(pid int) (string, error) { return process.Birth(pid) }
func (matrixKernelProbe) CommandLine(pid int) (string, error) {
	out, err := exec.Command("/bin/ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", process.CodeError("process_command_unknown")
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", process.CodeError("process_command_unknown")
	}
	return line, nil
}
func (matrixKernelProbe) Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// matrixCoordinator owns verified shutdown. It never signals a pid it cannot
// prove it started, and it never deletes a state directory while the coordinator
// may still hold the socket.
type matrixCoordinator struct {
	state string
	// root is the directory the cleanup would delete or move. The holder scan is
	// aimed at it, not at the state subdirectory, because the marker file, the
	// request file and the provider's declared workspace all live beside the state
	// directory and are removed with it.
	root  string
	pid   int
	birth string
	// expectedCommand is the argv fragment this case asked the orchestrator to run.
	// ps lstart resolves to one second, so a pid recycled inside that second keeps
	// an identical birth and has to be caught on the command line instead. It is
	// matched as a substring because argv[0] spelling is not the harness's to fix.
	expectedCommand string
	probe           matrixProcessProbe
	// holders lists the pids still referencing this state directory. The
	// coordinator and the source host are started as sibling session leaders
	// (cmd/orchestrator/main.go:800 and :1155), so stopping one says nothing
	// about the other.
	holders     func(string) ([]int, error)
	holderGrace time.Duration
	exitGrace   time.Duration
}

// matrixCoordinatorPID reads <state>/coordinator.pid. absent is true only when
// the file is genuinely missing. Every other failure means the directory's owner
// is unknown, which is not the same as nobody being home.
func matrixCoordinatorPID(state string) (pid int, absent bool, err error) {
	body, err := os.ReadFile(filepath.Join(state, "coordinator.pid"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("coordinator pid file: %w", err)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if convErr != nil || pid <= 0 {
		return 0, false, fmt.Errorf("coordinator pid file %q is not a pid", strings.TrimSpace(string(body)))
	}
	return pid, false, nil
}

func (c *matrixCoordinator) stopVerified() (matrixStopOutcome, error) {
	if c == nil {
		return stopNoCoordinator, nil
	}
	if c.probe == nil {
		c.probe = matrixKernelProbe{}
	}
	if c.holders == nil {
		c.holders = matrixProcessScan
	}
	if c.pid <= 0 {
		recorded, absent, err := matrixCoordinatorPID(c.state)
		switch {
		case absent:
			// No pid file says nothing about the source host, which the submit CLI
			// starts as its own session. The holder scan is what may free this.
			return c.awaitHolders(stopNotStarted)
		case err != nil:
			return stopPIDFileUnreadable, err
		default:
			return stopIdentityUnknown, fmt.Errorf("coordinator pid %d only surfaced at cleanup, so no birth was ever captured for it; refusing to signal", recorded)
		}
	}
	birth, birthErr := c.probe.Birth(c.pid)
	aliveErr := c.probe.Signal(c.pid, 0)
	if birthErr != nil {
		switch {
		case errors.Is(aliveErr, syscall.ESRCH):
			// No process can hold this pid, so there is nothing left to signal.
			return c.awaitHolders(stopExitedBeforeSignal)
		case aliveErr == nil, errors.Is(aliveErr, syscall.EPERM):
			return stopIdentityUnknown, fmt.Errorf("pid %d is running but its birth could not be read (%v); refusing to signal a process this case cannot pin", c.pid, birthErr)
		default:
			return stopProbeFailed, fmt.Errorf("pid %d: birth failed (%v) and existence is unknown (%v)", c.pid, birthErr, aliveErr)
		}
	}
	// The reference birth has to come from the start of the run. Reading it now
	// would compare a recycled pid against itself and call that an ownership proof.
	if c.birth == "" {
		return stopIdentityUnknown, fmt.Errorf("pid %d started without a recorded birth, so the birth read now (%s) proves nothing; refusing to signal", c.pid, birth)
	}
	if birth != c.birth {
		return stopPIDRecycled, fmt.Errorf("pid %d birth changed (%s -> %s); refusing to signal a recycled pid", c.pid, c.birth, birth)
	}
	// lstart resolves to one second, so an unchanged birth alone cannot rule out a
	// recycle inside that second. The argv this harness itself requested is the
	// second half of the identity proof.
	if c.expectedCommand != "" {
		command, commandErr := c.probe.CommandLine(c.pid)
		if commandErr != nil {
			return stopProbeFailed, fmt.Errorf("pid %d carries the recorded birth but its command line could not be read (%v); a same-second recycle would be indistinguishable", c.pid, commandErr)
		}
		if !strings.Contains(command, c.expectedCommand) {
			return stopPIDRecycled, fmt.Errorf("pid %d carries the recorded birth but runs %q, not %q; refusing to signal", c.pid, command, c.expectedCommand)
		}
	}
	recorded, absent, pidErr := matrixCoordinatorPID(c.state)
	switch {
	case pidErr != nil:
		return stopPIDFileUnreadable, pidErr
	case !absent && recorded != c.pid:
		return stopPIDFileMismatch, fmt.Errorf("pid file says %d, this case started %d; refusing to signal", recorded, c.pid)
	}
	if err := c.probe.Signal(c.pid, syscall.SIGTERM); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return stopSignalFailed, err
		}
		return c.awaitHolders(stopExitedBeforeSignal)
	}
	switch err := c.waitGone(c.grace()); {
	case err == nil:
		return c.awaitHolders(stopExited)
	case errors.Is(err, errCoordinatorStillRunning):
		// The proof above predates SIGTERM. A coordinator that exited on TERM
		// leaves its pid free for reuse during the whole graceful window, so
		// identity is proven again before the stronger signal. An identity
		// that can no longer be proven is never killed and its scene stays.
		if outcome, reErr, exited := c.reverifyForKill(); exited {
			return c.awaitHolders(stopExited)
		} else if reErr != nil {
			return outcome, reErr
		}
		if killErr := c.probe.Signal(c.pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return stopSignalFailed, fmt.Errorf("coordinator %d did not exit within %s and could not be killed: %w", c.pid, c.grace(), killErr)
		}
		if still := c.waitGone(c.grace()); still == nil {
			return c.awaitHolders(stopExited)
		} else {
			return stopExitTimeout, fmt.Errorf("coordinator %d survived TERM and KILL (%v)", c.pid, still)
		}
	case errors.Is(err, syscall.EPERM):
		return stopIdentityUnknown, fmt.Errorf("pid %d stopped belonging to this user after the term request", c.pid)
	default:
		return stopExitUnverified, err
	}
}

// reverifyForKill re-proves, after the graceful window, that c.pid still
// names the coordinator this case started: recorded birth, requested argv
// and pid file must all still agree. exited reports that the pid vanished
// while it was being re-proved, so no kill is needed at all. The checks and
// the kill that follows are not atomic: a reuse inside the few probe
// round-trips after the last check remains as a known residual, down from
// the whole graceful window, and macOS pid wraparound keeps it narrow.
func (c *matrixCoordinator) reverifyForKill() (matrixStopOutcome, error, bool) {
	birth, birthErr := c.probe.Birth(c.pid)
	if birthErr != nil {
		aliveErr := c.probe.Signal(c.pid, 0)
		switch {
		case errors.Is(aliveErr, syscall.ESRCH):
			return stopExited, nil, true
		case aliveErr == nil, errors.Is(aliveErr, syscall.EPERM):
			return stopIdentityUnknown, fmt.Errorf("pid %d is running but its birth could not be re-read before kill (%v); refusing to signal a process this case cannot pin", c.pid, birthErr), false
		default:
			return stopProbeFailed, fmt.Errorf("pid %d: birth re-read failed (%v) and existence is unknown (%v)", c.pid, birthErr, aliveErr), false
		}
	}
	if c.birth == "" || birth != c.birth {
		return stopPIDRecycled, fmt.Errorf("pid %d birth changed during the graceful window (%s -> %s); refusing to kill a recycled pid", c.pid, c.birth, birth), false
	}
	if c.expectedCommand != "" {
		command, commandErr := c.probe.CommandLine(c.pid)
		if commandErr != nil {
			return stopProbeFailed, fmt.Errorf("pid %d carries the recorded birth but its command line could not be re-read before kill (%v); a same-second recycle would be indistinguishable", c.pid, commandErr), false
		}
		if !strings.Contains(command, c.expectedCommand) {
			return stopPIDRecycled, fmt.Errorf("pid %d carries the recorded birth but now runs %q, not %q; refusing to kill", c.pid, command, c.expectedCommand), false
		}
	}
	recorded, absent, pidErr := matrixCoordinatorPID(c.state)
	switch {
	case pidErr != nil:
		return stopPIDFileUnreadable, pidErr, false
	case !absent && recorded != c.pid:
		return stopPIDFileMismatch, fmt.Errorf("pid file changed during the graceful window and now says %d, this case started %d; refusing to kill", recorded, c.pid), false
	}
	return "", nil, false
}

var errCoordinatorStillRunning = errors.New("coordinator still running")

// waitGone polls signal 0 until the pid is gone. It returns nil on absence,
// errCoordinatorStillRunning when the window closes, and the kernel's own error
// when the question cannot be answered.
func (c *matrixCoordinator) waitGone(window time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		err := c.probe.Signal(c.pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errCoordinatorStillRunning
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitHolders waits for every process still naming this case's root directory to
// leave it. Stopping the coordinator alone is not enough: the source host is started as
// a sibling session leader (cmd/orchestrator/main.go:1155, next to the
// coordinator's own :800), so it survives the coordinator and keeps the socket, the
// spool and the state database open.
//
// Scope of the proof, so it is not over-read: the scan sees a process only while
// its argv still names the root. That covers the coordinator, the source host and
// a provider child such as AGY, whose argv carries the declared workspace. It does
// not cover a provider that replaced its argv with provider-only arguments and
// still holds scratch files open; that tier is settled only by the host's own
// StopAll cascade (internal/host/source.go:342-351, and internal/host/host.go:1140
// discards the error Stop returns).
func (c *matrixCoordinator) awaitHolders(after matrixStopOutcome) (matrixStopOutcome, error) {
	wait := c.holderGrace
	if wait <= 0 {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	target := c.root
	if target == "" {
		target = c.state
	}
	var lastSeen []int
	clear := 0
	for {
		holders, err := c.holders(target)
		if err != nil {
			return stopProbeFailed, fmt.Errorf("%s could not be scanned: %w", target, err)
		}
		if len(holders) == 0 {
			// One empty scan is not enough: the snapshot can be taken a moment before
			// a process execs into this case's paths, so clearing needs two in a row.
			clear++
			if clear >= 2 {
				return after, nil
			}
		} else {
			clear, lastSeen = 0, holders
			if time.Now().After(deadline) {
				return stopHolderSurvives, fmt.Errorf("coordinator %d is gone but pids %v still reference %s; leaving the scene in place", c.pid, lastSeen, target)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *matrixCoordinator) grace() time.Duration {
	if c.exitGrace > 0 {
		return c.exitGrace
	}
	return 12 * time.Second
}

// matrixCloseOutCleanup classifies the exit of the process set this case started
// and then carries out the resulting disposition. It is a function rather than the
// body of runMatrixCase's deferred closure because the delete/rename decision is
// the part that can destroy evidence, and that has to be reachable from default
// automation instead of only from an opt-in real round.
func matrixCloseOutCleanup(coord *matrixCoordinator, root, repo string, record *matrixRecord, started time.Time) {
	outcome, stopErr := coord.stopVerified()
	record.Cleanup = string(outcome)
	if stopErr != nil {
		record.Cleanup += ": " + stopErr.Error()
	}
	switch matrixCleanupPlan(outcome, record.OK) {
	case cleanupRemoveState:
		_ = os.RemoveAll(root)
		record.Cleanup += "; state_removed"
	case cleanupKeepInPlace:
		record.PreservedDir = root
		record.Cleanup += "; state_preserved_in_place"
	case cleanupMoveScene:
		if scene := matrixPreserve(root, repo, record.Name, started); scene != "" {
			record.PreservedDir = scene
			record.Cleanup += "; failure_scene_preserved"
		} else {
			record.PreservedDir = root
			record.Cleanup += "; failure_scene_kept_in_place"
		}
	}
}

// matrixPreserve keeps a failure scene under the gitignored task tmp directory
// so the raw spool survives the round without entering tracked evidence.
func matrixPreserve(root, repo, name string, started time.Time) string {
	scene := filepath.Join(repo, "tasks/cli-version-compatibility/tmp/failure-scenes",
		started.UTC().Format("20060102T150405Z")+"-"+name)
	if err := os.MkdirAll(filepath.Dir(scene), 0700); err != nil {
		return ""
	}
	if err := os.Rename(root, scene); err != nil {
		return ""
	}
	return scene
}

// matrixSpoolRelative splits path below <state>/host-spool. The Host's own
// metadata lives at fixed depths below it: the segment ledger and summary in
// host-spool/<launch>/<attempt>/<segment>/, and the report capability in
// host-spool/<launch>/.reports/<id>/. A file with the same basename anywhere
// else — the segment scratch directory, the report artifact directory — sits
// in provider-writable space and is not Host evidence.
func matrixSpoolRelative(state, path string) ([]string, bool) {
	relative, err := filepath.Rel(filepath.Join(state, "host-spool"), path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false
	}
	return strings.Split(relative, string(filepath.Separator)), true
}

func matrixIsSegmentMetadata(state, path, name string) bool {
	parts, ok := matrixSpoolRelative(state, path)
	if !ok || len(parts) != 4 || parts[3] != name {
		return false
	}
	for _, part := range parts[:3] {
		if part == ".reports" || part == "scratch" {
			return false
		}
	}
	return true
}

func matrixIsReportCapability(state, path string) bool {
	parts, ok := matrixSpoolRelative(state, path)
	return ok && len(parts) == 4 && parts[1] == ".reports" && parts[3] == "capability.json" &&
		parts[0] != ".reports" && parts[0] != "scratch" && parts[2] != "" && parts[2] != "." && parts[2] != ".."
}

// matrixCapabilityVerdict recomputes the production report-capability check
// for one concrete capability path: resolve it, compare it with the resolved
// report directory of its own launch, and require the capability id to
// survive resolution.
func matrixCapabilityVerdict(capability string) (string, string) {
	spoolRoot := filepath.Dir(filepath.Dir(filepath.Dir(capability)))
	resolvedCapability, capabilityErr := filepath.EvalSymlinks(capability)
	resolvedRoot, rootErr := filepath.EvalSymlinks(spoolRoot)
	switch {
	case capabilityErr != nil:
		return "reject", ""
	case rootErr != nil:
		return "launch_root_unresolvable", ""
	default:
		reports := filepath.Join(resolvedRoot, ".reports")
		relative, relErr := filepath.Rel(reports, resolvedCapability)
		parts := strings.Split(relative, string(filepath.Separator))
		idKept := filepath.Base(capability) == "capability.json" &&
			filepath.Dir(resolvedCapability) == filepath.Join(reports, filepath.Base(filepath.Dir(capability)))
		if relErr == nil && len(parts) == 2 && parts[0] != ".." && parts[1] == "capability.json" && idKept {
			return "pass", relative
		}
		return "reject", relative
	}
}

func matrixDiagnose(state string) *matrixDiagnostics {
	diagnostics := &matrixDiagnostics{CapabilityPathCheck: "not_evaluated"}
	if canonical, err := filepath.EvalSymlinks(state); err == nil {
		diagnostics.StateDirSymlinkPrefix = canonical != state
	}
	var capabilities, launches, diagnosticsFiles []string
	_ = filepath.Walk(filepath.Join(state, "host-spool"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		switch {
		case base == "capability.json":
			if matrixIsReportCapability(state, path) {
				capabilities = append(capabilities, path)
			}
		case base == "launch.json":
			if matrixIsSegmentMetadata(state, path, "launch.json") {
				launches = append(launches, path)
			}
		case base == "provider-diagnostics.json":
			if matrixIsSegmentMetadata(state, path, "provider-diagnostics.json") {
				diagnosticsFiles = append(diagnosticsFiles, path)
			}
		case strings.HasPrefix(base, "event-"):
			// Only the Host's own per-segment spool counts as evidence. The scratch
			// directory and the report artifact directory are places a provider can
			// create files it names, and a file there shaped like an event would
			// otherwise speak for the run.
			if !matrixIsDurableSpoolEvent(path) {
				return nil
			}
			var event contract.Event
			if body, readErr := os.ReadFile(path); readErr == nil && json.Unmarshal(body, &event) == nil &&
				event.RunID == matrixRunID && event.TaskID == matrixTaskID {
				diagnostics.EventKinds = append(diagnostics.EventKinds, strconv.FormatInt(event.Sequence, 10)+":"+matrixExcerpt(redactSecrets(event.Kind)))
				if event.ExitCode != nil && diagnostics.ProcessExitCode == nil {
					exit := *event.ExitCode
					diagnostics.ProcessExitCode = &exit
				}
				switch event.Kind {
				case contract.EventPrepared:
					diagnostics.PreparedEventPresent = true
				case contract.EventSpawned:
					diagnostics.SpawnedEventPresent = true
				case contract.EventStopped:
					// The reason and the budget are the only record of why a run that
					// reached session state never produced a result.
					diagnostics.StopEvents = append(diagnostics.StopEvents, fmt.Sprintf("%d:%s:%dms", event.Sequence, matrixExcerpt(redactSecrets(event.StopReason)), event.ActiveMS))
				}
			}
		}
		return nil
	})
	diagnostics.ReportCapabilityPresent = len(capabilities) > 0
	if launch := matrixSolePath(launches, "launch_ledger", &diagnostics.AmbiguousSources); launch != "" {
		var phase map[string]any
		if body, readErr := os.ReadFile(launch); readErr == nil && json.Unmarshal(body, &phase) == nil {
			diagnostics.LaunchPhase, _ = phase["phase"].(string)
			diagnostics.LaunchCauseCode, _ = phase["cause_code"].(string)
			if executable, ok := phase["executable"].(string); ok && executable != "" {
				diagnostics.LaunchExecutable = filepath.Base(executable)
				diagnostics.LaunchExecutableRecorded = true
			}
		}
	}
	// The provider's own protocol summary is the only place that distinguishes a
	// provider-side refusal from a Host-side failure. Only counters and shapes are
	// kept: the summary is reduced to an allowlist of non-text keys so provider
	// output can never reach tracked evidence, and the retained text still passes
	// through redaction.
	if diagnosticsFile := matrixSolePath(diagnosticsFiles, "provider_diagnostics", &diagnostics.AmbiguousSources); diagnosticsFile != "" {
		if body, readErr := os.ReadFile(diagnosticsFile); readErr == nil {
			var summary map[string]any
			if json.Unmarshal([]byte(redactSecrets(string(body))), &summary) == nil {
				diagnostics.ProviderDiagnostics = matrixSafeCounters(summary)
			}
		}
	}
	sort.Strings(diagnostics.EventKinds)
	// Replicate sandboxCommand's report-capability path check against the paths
	// actually on disk, the way the guard itself performs it: resolve the
	// capability path, compare it with the resolved report directory, and
	// require the capability id to survive resolution. The result is the
	// harness's own recomputation, not the guard's verdict, and is labelled
	// as such wherever it is read.
	if capability := matrixSolePath(capabilities, "report_capability", &diagnostics.AmbiguousSources); capability != "" {
		diagnostics.CapabilityPathCheck, diagnostics.CapabilityRel = matrixCapabilityVerdict(capability)
	}
	switch text := strings.TrimSpace(string(readMaybe(filepath.Join(state, "source-host.log")))); {
	case text == "":
		diagnostics.HostLogClassification = "empty"
	case strings.Contains(text, `"error":"EOF"`):
		// The coordinator closes the host connection once the run settles, so a
		// trailing EOF is a shutdown symptom and not by itself the first failure.
		diagnostics.HostLogClassification = "eof_after_run_settled"
	default:
		diagnostics.HostLogClassification = "other"
	}
	return diagnostics
}

// matrixSpoolInventory records structural evidence only. Report capability
// material carries a bearer token and is never excerpted.
func matrixSpoolInventory(state string) []matrixSpoolEntry {
	var entries []matrixSpoolEntry
	truncated := false
	stopWalk := errors.New("matrix spool inventory complete")
	_ = filepath.Walk(filepath.Join(state, "host-spool"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if len(entries) >= matrixSpoolCap {
			truncated = true
			return stopWalk
		}
		relative, relErr := filepath.Rel(state, path)
		if relErr != nil {
			relative = filepath.Base(path)
		}
		entry := matrixSpoolEntry{Path: relative, Size: info.Size(), Mode: info.Mode().String()}
		switch {
		case strings.Contains(relative, ".reports") || filepath.Base(path) == "capability.json":
			// The provider never needs its capability body in tracked evidence,
			// and this harness must not become the thing that leaks it.
			entry.Withheld = "report_capability_material_withheld"
		case strings.Contains(relative, "session-intent"):
			// Session-intent files carry the Provider session id plus the session
			// and socket directories under the operator's home. Not credentials,
			// but they are provider session material and the batch forbids copying
			// session or authentication environment into deliverables.
			entry.Withheld = "provider_session_material_withheld"
		case info.Size() == 0 || info.Size() > 8192:
			entry.Withheld = "empty_or_too_large"
		default:
			body, readErr := os.ReadFile(path)
			if readErr != nil || strings.IndexByte(string(body), 0) >= 0 {
				entry.Withheld = "unreadable_or_binary"
			} else {
				entry.Excerpt = matrixExcerpt(redactSecrets(strings.TrimSpace(string(body))))
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if truncated {
		entries = append(entries, matrixSpoolEntry{Path: fmt.Sprintf("(inventory truncated at %d entries)", matrixSpoolCap), Withheld: "truncated"})
	}
	return entries
}

var matrixSecretPatterns = []struct {
	expression  *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(report-token-)[0-9a-fA-F]+`), "${1}REDACTED"},
	{regexp.MustCompile(`(control-token-)[0-9a-fA-F]+`), "${1}REDACTED"},
	{regexp.MustCompile(`(host-token-)[0-9a-fA-F]+`), "${1}REDACTED"},
	{regexp.MustCompile(`(slot-token-)[0-9a-fA-F]+`), "${1}REDACTED"},
	{regexp.MustCompile(`("(?:token|control_token|launch_token|slot_token|auth_token|api_key|authorization)"\s*:\s*")[^"]*(")`), "${1}REDACTED${2}"},
	// Backstop for the session-intent withholding above: a Provider session id
	// or a session/socket directory under the operator's home must not survive in
	// any excerpt, whichever file it turns up in.
	{regexp.MustCompile(`("(?:session_id|session_directory|socket_directory|conversation_id)"\s*:\s*")[^"]*(")`), "${1}REDACTED${2}"},
	{regexp.MustCompile(`/Users/[^/"\s]+/\.grok/sessions/\S+`), "REDACTED_PROVIDER_SESSION_PATH"},
	{regexp.MustCompile(`/Users/[^/"\s]+/\.codex/[^"\s]*`), "REDACTED_PROVIDER_CODEX_PATH"},
	{regexp.MustCompile(`/Users/[^/"\s]+/\.claude[^"\s]*`), "REDACTED_PROVIDER_CLAUDE_PATH"},
}

func redactSecrets(text string) string {
	out := text
	for _, pattern := range matrixSecretPatterns {
		out = pattern.expression.ReplaceAllString(out, pattern.replacement)
	}
	return out
}

func matrixExcerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 600 {
		return text[:600] + "…"
	}
	return text
}

func matrixGuardFrom(output []byte) *matrixGuard {
	var payload struct {
		Error   string   `json:"error"`
		Reason  string   `json:"reason"`
		Missing []string `json:"missing"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return nil
	}
	if payload.Error != "codex_trial_guard_not_ready" && payload.Reason != "codex_trial_guard_not_ready" {
		return nil
	}
	code := payload.Error
	if code == "" {
		code = payload.Reason
	}
	return &matrixGuard{Code: code, Missing: payload.Missing, TrialComplete: false}
}

// matrixSafeCounterKeys are the counter and shape fields of the provider
// protocol summary. Fields outside this list are dropped outright.
var matrixSafeCounterKeys = []string{
	"captured_bytes", "truncated", "structured_results", "events", "codex_auth_guard_codes",
	"result_shapes", "system_subtypes", "tools", "tool_errors",
	"permission_denials", "stream_denials", "protocol_snapshots",
}

func matrixSafeCounters(summary map[string]any) map[string]any {
	kept := make(map[string]any, len(matrixSafeCounterKeys))
	for _, key := range matrixSafeCounterKeys {
		value, ok := summary[key]
		if !ok {
			continue
		}
		if cleaned := withoutStrings(value); cleaned != nil {
			kept[key] = cleaned
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// withoutStrings drops string leaves so a provider's own message text cannot be
// carried into tracked evidence. What survives is counts, booleans and the
// object structure around them, which is enough to tell a provider-side refusal
// apart from a Host-side failure.
func withoutStrings(value any) any {
	switch typed := value.(type) {
	case string:
		return nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if cleaned := withoutStrings(item); cleaned != nil {
				out[key] = cleaned
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if cleaned := withoutStrings(item); cleaned != nil {
				out = append(out, cleaned)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return value
	}
}

func matrixMarker() (string, error) {
	body := make([]byte, 12)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return "MATRIX-" + hex.EncodeToString(body), nil
}

func matrixSHA256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func matrixToolchainRecord(repo string) matrixToolchain {
	record := matrixToolchain{
		GoVersion:  runtime.Version(),
		Goos:       runtime.GOOS,
		Goarch:     runtime.GOARCH,
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); err == nil {
		record.GitHead = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); err == nil {
		record.GitDirty = len(strings.TrimSpace(string(out))) > 0
	}
	return record
}

// matrixRepoRoot derives the checkout from the package directory instead of
// hardcoding an operator-specific path.
func matrixRepoRoot(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("ORCHESTRATOR_REPO_ROOT"); override != "" {
		resolved, err := filepath.EvalSymlinks(override)
		if err != nil || !filepath.IsAbs(resolved) {
			t.Fatalf("ORCHESTRATOR_REPO_ROOT unusable: %v", err)
		}
		return resolved
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.EvalSymlinks(filepath.Clean(filepath.Join(working, "..", "..", "..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "tools", "orchestrator", "go.mod")); statErr != nil {
		t.Fatalf("derived repository root %s does not look like this checkout: %v", repo, statErr)
	}
	return repo
}

// matrixTempRoot returns a symlink-canonical temporary root.
//
// The Host derives its sandbox rules from an EvalSymlinks'd scratch path, while
// the report capability path is built from the spool root exactly as handed
// over. A state directory with a symlinked prefix (macOS /tmp -> /private/tmp)
// makes the two diverge, sandboxCommand then rejects the launch with
// report_capability_path_untrusted, and the launch ledger records only the
// opaque cause "launch_failed". Canonicalizing here keeps one prefix end to end.
func matrixTempRoot(t *testing.T, label string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary base: %v", err)
	}
	root, err := os.MkdirTemp(base, label)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	return canonical
}

func readMaybe(path string) []byte {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return body
}

func quietStatus(ctx context.Context, runner matrixRunner, state, taskID, control string) cliTaskStatus {
	output, err := runner.cmd(ctx, "status", "--state-dir", state, "--task-id", taskID, "--control-file", control).CombinedOutput()
	var status cliTaskStatus
	if err == nil {
		_ = json.Unmarshal(output, &status)
	}
	return status
}

// Runs in default automation: it drives no provider and calls no model. These
// withholding rules decide what a real round may write into tracked evidence, so
// a leak has to turn this red instead of only appearing inside an opt-in matrix.
// matrixStopProbe scripts the two kernel questions stopVerified asks: what is
// this pid's birth, and is it still there. Real rounds never install one.
type matrixStopProbe struct {
	birth      string
	birthErr   error
	command    string
	commandErr error
	aliveErrs  []error
	stopErr    error
	killErr    error
	signals    []syscall.Signal
	birthRead  int
	aliveReads int
	// onTerm runs when SIGTERM is delivered. A case uses it to change the
	// scripted identity or the pid file during the graceful window, which is
	// exactly the interval in which a real pid can be recycled.
	onTerm func()
	// goneAtRecheck makes later existence probes report ESRCH once the birth
	// has been read for the second time, modelling a pid that vanishes while
	// its identity is being re-proved before SIGKILL.
	goneAtRecheck bool
}

func (p *matrixStopProbe) Birth(int) (string, error) {
	p.birthRead++
	if p.birthErr != nil {
		return "", p.birthErr
	}
	return p.birth, nil
}

func (p *matrixStopProbe) CommandLine(int) (string, error) {
	if p.commandErr != nil {
		return "", p.commandErr
	}
	return p.command, nil
}

// Signal answers a zero signal from aliveErrs in order and then repeats the last
// entry, so a scripted case can say "running at the identity probe, gone during
// the exit poll" without racing a real process.
func (p *matrixStopProbe) Signal(_ int, sig syscall.Signal) error {
	p.signals = append(p.signals, sig)
	switch sig {
	case 0:
		if p.goneAtRecheck && p.birthRead >= 2 {
			return syscall.ESRCH
		}
		at := p.aliveReads
		p.aliveReads++
		if at >= len(p.aliveErrs) {
			at = len(p.aliveErrs) - 1
		}
		if at < 0 {
			return nil
		}
		return p.aliveErrs[at]
	case syscall.SIGKILL:
		return p.killErr
	case syscall.SIGTERM:
		if p.onTerm != nil {
			p.onTerm()
		}
		return p.stopErr
	default:
		return p.stopErr
	}
}

func writeMatrixPIDFile(t *testing.T, state, body string) {
	t.Helper()
	if body == "" {
		return
	}
	path := filepath.Join(state, "coordinator.pid")
	if body == "unreadable" {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, []byte(body+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMatrixCoordinatorStopKeepsSceneWhenExitIsUnproven(t *testing.T) {
	const startBirth = "Mon Nov  2 10:00:00 2026"
	const otherBirth = "Mon Nov  2 10:00:01 2026"
	tests := []struct {
		name          string
		capturedPID   int
		capturedBirth string
		pidFile       string
		birth         string
		birthErr      error
		command       string
		commandErr    error
		aliveErrs     []error
		stopErr       error
		killErr       error
		holders       []int
		holderGrace   time.Duration
		exitGrace     time.Duration
		// Identity and pid file as they stand after SIGTERM, inside the
		// graceful window. Empty means unchanged.
		termBirth      string
		termBirthErr   error
		termCommand    string
		termCommandErr error
		termPIDFile    string
		// The pid vanishes while its identity is being re-proved.
		termGoneAtRecheck bool
		want              matrixStopOutcome
		wantSettled       bool
		wantTerminals     int
		wantKills         int
	}{
		{
			name: "birth query fails while the pid is absent",
			// Nothing can hold the pid, so there is nothing left to signal.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birthErr: process.CodeError("process_birth_unknown"), aliveErrs: []error{syscall.ESRCH},
			want: stopExitedBeforeSignal, wantSettled: true,
		},
		{
			name: "birth query fails while the pid is alive",
			// Alive but unidentifiable: signalling it could hit an innocent process.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birthErr: process.CodeError("process_birth_unknown"),
			want:     stopIdentityUnknown, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "birth query fails and the pid belongs to another user",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birthErr: process.CodeError("process_birth_unknown"), aliveErrs: []error{syscall.EPERM},
			want:        stopIdentityUnknown,
			wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "both kernel queries fail",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birthErr: process.CodeError("process_birth_unknown"), aliveErrs: []error{syscall.EIO},
			want: stopProbeFailed, wantSettled: false, wantTerminals: 0,
		},
		{
			name: "no birth was captured when the process started",
			// Backfilling the current birth here is what makes pid reuse invisible,
			// so the reference identity has to come from the start of the run.
			capturedPID: 4242, capturedBirth: "", pidFile: "4242",
			birth: otherBirth,
			want:  stopIdentityUnknown, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "the pid was recycled",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: otherBirth,
			want:  stopPIDRecycled, wantSettled: false, wantTerminals: 0,
		},
		{
			name: "the pid was recycled within the same second",
			// ps lstart only has one-second resolution, so an unchanged birth is not a
			// complete identity proof. The command line is the second half.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, command: "/sbin/launchd",
			want: stopPIDRecycled, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "the command line cannot be read",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, commandErr: process.CodeError("process_command_unknown"),
			want: stopProbeFailed, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "pid file unreadable for a reason other than absence",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "unreadable",
			birth: startBirth,
			want:  stopPIDFileUnreadable, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "pid file names a different process",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "9999",
			birth: startBirth,
			want:  stopPIDFileMismatch, wantSettled: false, wantTerminals: 0,
		},
		{
			name: "pid file is gone while the coordinator still runs",
			// The old shape reported this as a settled cleanup and deleted the state
			// directory out from under a live coordinator.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "",
			birth: startBirth, aliveErrs: []error{nil, syscall.ESRCH},
			want: stopExited, wantSettled: true, wantTerminals: 1,
		},
		{
			name:        "no pid was ever captured and no pid file exists",
			capturedPID: 0, pidFile: "",
			want: stopNotStarted, wantSettled: true, wantTerminals: 0,
		},
		{
			name:        "no pid was ever captured but the pid file is there now",
			capturedPID: 0, pidFile: "4242",
			want: stopIdentityUnknown, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "term request refused",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, stopErr: syscall.EPERM,
			want: stopSignalFailed, wantSettled: false, wantTerminals: 1,
		},
		{
			name: "source host survives the coordinator",
			// The host is a sibling session leader, not a child, so the coordinator
			// exiting alone never empties the state directory.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, aliveErrs: []error{nil, syscall.ESRCH}, holders: []int{4300},
			holderGrace: 30 * time.Millisecond,
			want:        stopHolderSurvives, wantSettled: false, wantTerminals: 1,
		},
		{
			name: "no pid captured but a host still holds the state directory",
			// An absent coordinator.pid says nothing about the source host, which the
			// submit CLI starts as its own session. This must not be a license to clean.
			capturedPID: 0, pidFile: "", holders: []int{4300},
			holderGrace: 30 * time.Millisecond,
			want:        stopHolderSurvives, wantSettled: false, wantTerminals: 0,
		},
		{
			name:        "exit poll cannot answer",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, aliveErrs: []error{nil, syscall.EIO}, exitGrace: 2 * time.Second,
			want: stopExitUnverified, wantSettled: false, wantTerminals: 1,
		},
		{
			name: "coordinator ignores the term request",
			// Identity is proven here, so escalating to KILL is safe; a process that
			// survives even that is reported, never treated as cleaned.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, exitGrace: 60 * time.Millisecond,
			want: stopExitTimeout, wantSettled: false, wantTerminals: 1, wantKills: 1,
		},
		{
			name: "pid recycled during the graceful window",
			// The coordinator exits on TERM and an unrelated process takes the
			// pid before the window closes. Killing whatever now owns the pid
			// would be a misdirected kill, so identity is proven again first.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termBirth: otherBirth, exitGrace: 60 * time.Millisecond,
			want: stopPIDRecycled, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name:        "pid recycled within the same second during the window",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termCommand: "/sbin/launchd", exitGrace: 60 * time.Millisecond,
			want: stopPIDRecycled, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name:        "birth unreadable during the graceful window",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termBirthErr: process.CodeError("process_birth_unknown"), exitGrace: 60 * time.Millisecond,
			want: stopIdentityUnknown, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name:        "command line unreadable during the graceful window",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termCommandErr: process.CodeError("process_command_unknown"), exitGrace: 60 * time.Millisecond,
			want: stopProbeFailed, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name:        "pid file retargeted during the graceful window",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termPIDFile: "9999", exitGrace: 60 * time.Millisecond,
			want: stopPIDFileMismatch, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name:        "pid file unreadable during the graceful window",
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termPIDFile: "unreadable", exitGrace: 60 * time.Millisecond,
			want: stopPIDFileUnreadable, wantSettled: false, wantTerminals: 1, wantKills: 0,
		},
		{
			name: "pid vanishes while being re-proved before the kill",
			// Nothing is left to kill, so the holders alone decide the scene.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termBirthErr: process.CodeError("process_birth_unknown"), termGoneAtRecheck: true, exitGrace: 60 * time.Millisecond,
			want: stopExited, wantSettled: true, wantTerminals: 1, wantKills: 0,
		},
		{
			name: "pid file deleted during the graceful window",
			// An absent file contradicts nothing, exactly as before TERM: the
			// identity still stands on birth and argv, so the kill proceeds.
			capturedPID: 4242, capturedBirth: startBirth, pidFile: "4242",
			birth: startBirth, termPIDFile: "deleted", exitGrace: 60 * time.Millisecond,
			want: stopExitTimeout, wantSettled: false, wantTerminals: 1, wantKills: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			writeMatrixPIDFile(t, state, tc.pidFile)
			expect := "serve --state-dir " + state
			command := tc.command
			if command == "" {
				command = expect
			}
			probe := &matrixStopProbe{birth: tc.birth, birthErr: tc.birthErr, command: command, commandErr: tc.commandErr, aliveErrs: tc.aliveErrs, stopErr: tc.stopErr, killErr: tc.killErr, goneAtRecheck: tc.termGoneAtRecheck}
			if tc.termBirth != "" || tc.termBirthErr != nil || tc.termCommand != "" || tc.termCommandErr != nil || tc.termPIDFile != "" {
				probe.onTerm = func() {
					if tc.termBirth != "" {
						probe.birth = tc.termBirth
					}
					if tc.termBirthErr != nil {
						probe.birthErr = tc.termBirthErr
					}
					if tc.termCommand != "" {
						probe.command = tc.termCommand
					}
					if tc.termCommandErr != nil {
						probe.commandErr = tc.termCommandErr
					}
					if tc.termPIDFile == "deleted" {
						_ = os.Remove(filepath.Join(state, "coordinator.pid"))
					} else if tc.termPIDFile == "unreadable" {
						path := filepath.Join(state, "coordinator.pid")
						_ = os.Remove(path)
						_ = os.Mkdir(path, 0700)
					} else if tc.termPIDFile != "" {
						_ = os.WriteFile(filepath.Join(state, "coordinator.pid"), []byte(tc.termPIDFile+"\n"), 0600)
					}
				}
			}
			coord := &matrixCoordinator{
				state: state, pid: tc.capturedPID, birth: tc.capturedBirth,
				expectedCommand: expect,
				probe:           probe, holders: func(string) ([]int, error) { return tc.holders, nil },
				holderGrace: tc.holderGrace, exitGrace: tc.exitGrace,
			}
			got, err := coord.stopVerified()
			if got != tc.want {
				t.Errorf("outcome = %q (err %v), want %q", got, err, tc.want)
			}
			if got.settled() != tc.wantSettled {
				t.Errorf("%q settled() = %t, want %t", got, got.settled(), tc.wantSettled)
			}
			terminals, kills := 0, 0
			for _, sig := range probe.signals {
				switch sig {
				case syscall.SIGTERM:
					terminals++
				case syscall.SIGKILL:
					kills++
				}
			}
			if terminals != tc.wantTerminals {
				t.Errorf("SIGTERM delivered %d times, want %d (signals %v)", terminals, tc.wantTerminals, probe.signals)
			}
			if kills != tc.wantKills {
				t.Errorf("SIGKILL delivered %d times, want %d (signals %v)", kills, tc.wantKills, probe.signals)
			}
			if matrixCleanupPlan(got, true) == cleanupRemoveState && !tc.wantSettled {
				t.Errorf("%q would delete a state directory whose owner is unproven", got)
			}
		})
	}
	var absent *matrixCoordinator
	if got, err := absent.stopVerified(); got != stopNoCoordinator || err != nil || !got.settled() {
		t.Errorf("run that never reached the coordinator: outcome %q err %v settled %t, want %q settled with no error", got, err, got.settled(), stopNoCoordinator)
	}
}

func TestMatrixCoordinatorStopWaitsForSurvivingHolders(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	writeMatrixPIDFile(t, state, "4242")
	probe := &matrixStopProbe{birth: "Mon Nov  2 10:00:00 2026", aliveErrs: []error{nil, syscall.ESRCH}}
	var scans int
	coord := &matrixCoordinator{
		state: state, pid: 4242, birth: probe.birth,
		probe:       probe,
		holderGrace: 250 * time.Millisecond,
		holders: func(string) ([]int, error) {
			scans++
			if scans < 3 {
				return []int{4300}, nil
			}
			return nil, nil
		},
	}
	got, err := coord.stopVerified()
	if got != stopExited || err != nil {
		t.Fatalf("outcome = %q err %v, want %q with no error once the host leaves", got, err, stopExited)
	}
	// Two empty scans in a row are required before the scene may be freed, so the
	// third scan alone must not be enough.
	if scans < 4 {
		t.Fatalf("holder set was scanned %d times; clearing needs two consecutive empty scans", scans)
	}
}

func TestMatrixCleanupPlanMatchesStopOutcome(t *testing.T) {
	for _, outcome := range []matrixStopOutcome{
		stopNoCoordinator, stopNotStarted, stopExited, stopExitedBeforeSignal,
	} {
		if !outcome.settled() {
			t.Errorf("%q must free the state directory", outcome)
		}
		if got := matrixCleanupPlan(outcome, true); got != cleanupRemoveState {
			t.Errorf("%q accepted run: cleanup = %q, want %q", outcome, got, cleanupRemoveState)
		}
		if got := matrixCleanupPlan(outcome, false); got != cleanupMoveScene {
			t.Errorf("%q failed run: cleanup = %q, want %q", outcome, got, cleanupMoveScene)
		}
	}
	for _, outcome := range []matrixStopOutcome{
		stopIdentityUnknown, stopProbeFailed, stopPIDRecycled, stopPIDFileUnreadable,
		stopPIDFileMismatch, stopSignalFailed, stopExitUnverified, stopExitTimeout,
		stopHolderSurvives,
	} {
		if outcome.settled() {
			t.Errorf("%q is not a proof of exit", outcome)
		}
		if got := matrixCleanupPlan(outcome, true); got != cleanupKeepInPlace {
			t.Errorf("%q accepted run: cleanup = %q, want %q", outcome, got, cleanupKeepInPlace)
		}
		if got := matrixCleanupPlan(outcome, false); got != cleanupKeepInPlace {
			t.Errorf("%q failed run: cleanup = %q, want %q", outcome, got, cleanupKeepInPlace)
		}
	}
}

// matrixProcessScan lists the pids whose command line names this exact state
// directory or anything underneath it. That is how the harness sees the process
// set it created without a provider-level hook; see matrixReferencesPath for what
// each tier's argv actually looks like.
func matrixProcessScan(state string) ([]int, error) {
	out, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("process scan: %w", err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if !matrixReferencesPath(line, state) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, convErr := strconv.Atoi(fields[0])
		if convErr != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// matrixReferencesPath reports whether an argv line names this exact path or
// anything underneath it. The coordinator and the source host carry --state-dir,
// but the provider child is launched as sandbox-exec with the SBPL policy as an
// argument, and that policy embeds the state directory even though the flag does
// not appear, so matching only the flag would leave the last tier invisible.
func matrixReferencesPath(line, path string) bool {
	for start := 0; ; {
		at := strings.Index(line[start:], path)
		if at < 0 {
			return false
		}
		at += start
		end := at + len(path)
		preceded := at == 0 || (line[at-1] != '/' && !matrixPathNameByte(line[at-1]))
		followed := end == len(line) || line[end] == '/' || !matrixPathNameByte(line[end])
		if preceded && followed {
			return true
		}
		start = at + 1
	}
}

// matrixPathNameByte reports whether b can continue a path element. Quotes,
// spaces, parentheses and separators end it, so a directory that merely prefixes
// this case's path, or one it is a prefix of, is not read as a reference to it.
func matrixPathNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("_-.~@%+:", b) >= 0
}

func TestMatrixProcessScanReportsOnlyThisCaseRoot(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	other := filepath.Join(root, "state-sibling")
	shorter := filepath.Join(root, "stat")
	work := filepath.Join(root, "work")
	for _, dir := range []string{state, other, shorter, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Two holder shapes, each standing for a real tier. A shell that does not exec
	// keeps its own argv: --state-dir names the coordinator and the source host,
	// while an inline SBPL policy names the sandboxed provider child, whose argv
	// never carries that flag.
	flagged := exec.Command("/bin/sh", "-c", "sleep 20; exit 0", "holder", "--state-dir", state)
	policy := exec.Command("/bin/sh", "-c", "sleep 20; exit 0", "sandbox-exec", "-p", "(allow file-write* (subpath "+strconv.Quote(work)+"))", "/bin/antigravity", "--add-dir", work)
	for _, holder := range []*exec.Cmd{flagged, policy} {
		holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := holder.Start(); err != nil {
			t.Fatal(err)
		}
		// The sleep runs as the holder's child, so the whole group has to be reaped
		// instead of being orphaned for the rest of the session.
		t.Cleanup(func() { _ = syscall.Kill(-holder.Process.Pid, syscall.SIGKILL) })
	}
	// A finished process must stop counting: that is the difference between waiting
	// out a grace period and never freeing the scene.
	exited := exec.Command("/bin/sh", "-c", "exit 0", "gone", "--state-dir", other)
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	holders, err := matrixProcessScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 2 || !slices.Contains(holders, flagged.Process.Pid) || !slices.Contains(holders, policy.Process.Pid) {
		t.Fatalf("scan for %s reported %v, want exactly [%d %d]", root, holders, flagged.Process.Pid, policy.Process.Pid)
	}
	// The state subdirectory is named by the coordinator tier only: the policy
	// holder references the workspace beside it. A directory that merely prefixes
	// the root, and one the root is prefixed by, belong to somebody else and stay
	// out of the result.
	for _, path := range []string{state, other, shorter} {
		found, err := matrixProcessScan(path)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if path == state {
			want = 1
		}
		if len(found) != want {
			t.Fatalf("scan for %s reported %v, want %d holder(s)", path, found, want)
		}
	}
}

func TestMatrixArtifactRejectsAnyContentAroundTheMarker(t *testing.T) {
	dir := t.TempDir()
	// A real marker is "MATRIX-" plus twelve random bytes in hex, so 31 bytes;
	// the passing rounds' artifacts were 32 because the marker file ends in a
	// newline. Both shapes are allowed here, nothing wider.
	const marker = "MATRIX-0123456789abcdef01234567"
	const otherRound = "MATRIX-fedcba9876543210fedcba98"
	tests := []struct {
		name       string
		body       string
		wantAccept bool
	}{
		{"exact file contents with its newline", marker + "\n", true},
		{"provider trimmed the trailing newline", marker, true},
		{"appended prose", marker + "\nThat is the content of the file.", false},
		{"leading prose", "The file says: " + marker, false},
		{"marker wrapped in a sentence", "answer=" + marker + " done", false},
		{"extra blank line", marker + "\n\n", false},
		{"trailing space", marker + " ", false},
		{"leading newline", "\n" + marker, false},
		{"previous round marker", otherRound + "\n", false},
		{"both rounds concatenated", otherRound + "\n" + marker + "\n", false},
		{"failure artifact", `{"status":"incomplete","reason":"provider_critical_event_too_large"}`, false},
		{"provider error text", "authentication_failed: Not logged in", false},
		{"empty answer", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".bin")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(tc.body))
			ref := &contract.ArtifactRef{ID: filepath.Base(path), Path: path, Size: int64(len(tc.body)), SHA256: hex.EncodeToString(sum[:])}
			artifact, err := matrixVerifyArtifact(ref, marker)
			if tc.wantAccept {
				if err != nil {
					t.Fatalf("refused the exact per-round marker: %v", err)
				}
				if artifact == nil || !artifact.ExactMatch {
					t.Fatalf("accepted without recording an exact match: %+v", artifact)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted content that is not exactly this round's marker: %q", tc.body)
			}
			if artifact != nil && artifact.ExactMatch {
				t.Fatalf("exact_match recorded true for %q", tc.body)
			}
		})
	}
	// The digest and size bindings have to survive the stricter content rule,
	// otherwise a tampered file could still pass on content alone.
	path := filepath.Join(dir, "digest.bin")
	body := marker + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	wrongDigest := &contract.ArtifactRef{ID: "result-output.bin", Path: path, Size: int64(len(body)), SHA256: strings.Repeat("0", 64)}
	if _, err := matrixVerifyArtifact(wrongDigest, marker); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest binding lost: %v", err)
	}
	wrongSize := &contract.ArtifactRef{ID: "result-output.bin", Path: path, Size: int64(len(body) - 1), SHA256: hex.EncodeToString(sum[:])}
	if _, err := matrixVerifyArtifact(wrongSize, marker); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("size binding lost: %v", err)
	}
	if _, err := matrixVerifyArtifact(nil, marker); err == nil {
		t.Fatal("artifact-free result event accepted")
	}
	// The reference has to be the registered file itself. The harness's own marker
	// file already holds exactly marker+"\n", so a reference redirected at it would
	// pass the digest, the size and the content rule on bytes no provider emitted.
	target := filepath.Join(dir, "workspace-marker.txt")
	if err := os.WriteFile(target, []byte(marker+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "redirected.bin")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	linkedSum := sha256.Sum256([]byte(marker + "\n"))
	redirected := &contract.ArtifactRef{ID: "result-output.bin", Path: linked, Size: int64(len(marker) + 1), SHA256: hex.EncodeToString(linkedSum[:])}
	if artifact, err := matrixVerifyArtifact(redirected, marker); err == nil {
		t.Fatalf("accepted a symlinked artifact reference: %+v", artifact)
	}
	// And the withheld-by-shape field must not survive into the record at all: a
	// weaker containment flag beside exact_match is something a reader can key on.
	body2, _ := json.Marshal(matrixArtifact{ExactMatch: true})
	if strings.Contains(string(body2), "marker_present") {
		t.Fatalf("evidence still carries the weaker containment flag: %s", body2)
	}
}

// A round that dies at the task deadline and a round that dies for any other
// reason have to stay distinguishable in the tracked record. Both AGY 1.2.3
// failures on 2026-11-02 reached prepared/spawned/running/session and were then
// stopped, and the reason only lives on the stopped event.
func TestMatrixDiagnoseKeepsTheStopBoundary(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	spool := filepath.Join(state, "host-spool", "launch", "attempt", "segment")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	one := 1
	// Real spool events carry the run and task (verified against the preserved
	// scenes), so the diagnostic binding below is not a harness-only convention.
	events := []contract.Event{
		{RunID: matrixRunID, TaskID: matrixTaskID, Sequence: 4, Kind: contract.EventSession},
		{RunID: matrixRunID, TaskID: matrixTaskID, Sequence: 5, Kind: contract.EventStopped, StopReason: "task_deadline_exceeded", ActiveMS: 150000},
		{RunID: matrixRunID, TaskID: matrixTaskID, Sequence: 6, Kind: contract.EventExited, ExitCode: &one},
	}
	for _, event := range events {
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("event-%020d.json", event.Sequence)
		if err = os.WriteFile(filepath.Join(spool, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	diagnostic := matrixDiagnose(state)
	if got := strings.Join(diagnostic.StopEvents, ","); got != "5:task_deadline_exceeded:150000ms" {
		t.Errorf("stop boundary recorded as %q, want the stopped sequence, reason and active budget", got)
	}
	if diagnostic.ProcessExitCode == nil || *diagnostic.ProcessExitCode != 1 {
		t.Errorf("exit code lost: %+v", diagnostic.ProcessExitCode)
	}
	if got := strings.Join(diagnostic.EventKinds, ","); !strings.Contains(got, "5:stopped") {
		t.Errorf("stopped event missing from the kind chain: %q", got)
	}
	// A run that finished without ever being stopped must not invent an entry.
	clean := filepath.Join(t.TempDir(), "state")
	cleanSpool := filepath.Join(clean, "host-spool", "launch", "attempt", "segment")
	if err := os.MkdirAll(cleanSpool, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(contract.Event{RunID: matrixRunID, TaskID: matrixTaskID, Sequence: 7, Kind: contract.EventResult})
	if err := os.WriteFile(filepath.Join(cleanSpool, "event-00000000000000000007.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if got := matrixDiagnose(clean).StopEvents; len(got) != 0 {
		t.Errorf("a result-only run reported a stop: %v", got)
	}
}

// The event scan must not be a channel from provider-writable files into tracked
// evidence, and it must not lose the real spool events either.
func TestMatrixDiagnoseIgnoresProviderWritableEventLookalikes(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	segment := filepath.Join(state, "host-spool", "launch", "attempt", "segment")
	scratch := filepath.Join(segment, "scratch")
	artifacts := filepath.Join(state, "host-spool", "launch", ".reports", "cap-1", "artifacts")
	for _, dir := range []string{segment, scratch, artifacts} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir string, sequence int64, event contract.Event) {
		event.Sequence = sequence
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("event-%020d.json", sequence)), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ours := func(kind, reason string) contract.Event {
		return contract.Event{RunID: matrixRunID, TaskID: matrixTaskID, Kind: kind, StopReason: reason}
	}
	write(segment, 4, ours(contract.EventSession, ""))
	write(segment, 5, ours(contract.EventStopped, "task_deadline_exceeded"))
	// Provider-writable lookalikes: the scratch directory and the report artifact
	// directory are both places a provider can create files it names itself.
	write(scratch, 6, ours(contract.EventResult, "SENTINEL_SCRATCH_TEXT"))
	write(artifacts, 7, ours(contract.EventStopped, "SENTINEL_REPORTS_TEXT"))
	// Right directory, wrong run: an event from a different run or task is not this
	// case's evidence either.
	write(segment, 8, contract.Event{RunID: "somebody-else", TaskID: "other", Kind: contract.EventStopped, StopReason: "OTHER_RUN_TEXT"})

	diagnostic := matrixDiagnose(state)
	joined := strings.Join(diagnostic.StopEvents, " ") + " " + strings.Join(diagnostic.EventKinds, " ")
	for _, forbidden := range []string{"SENTINEL_SCRATCH_TEXT", "SENTINEL_REPORTS_TEXT", "OTHER_RUN_TEXT"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("provider-writable or foreign event reached tracked evidence (%s): stops=%v kinds=%v", forbidden, diagnostic.StopEvents, diagnostic.EventKinds)
		}
	}
	if !strings.Contains(joined, "5:task_deadline_exceeded") || !strings.Contains(joined, "4:session") {
		t.Fatalf("legitimate segment events lost: stops=%v kinds=%v", diagnostic.StopEvents, diagnostic.EventKinds)
	}
}

// Metadata files carry the same risk as events: the ledger, the provider
// summary and the report capability are all named by basename alone today, so
// a provider can plant lookalikes in the directories it may write.
func TestMatrixDiagnoseIgnoresProviderWritableMetadataLookalikes(t *testing.T) {
	writeFile := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(t.TempDir(), "state")
	segment := filepath.Join(state, "host-spool", "launch", "attempt", "segment")
	scratch := filepath.Join(segment, "scratch")
	reports := filepath.Join(state, "host-spool", "launch", ".reports", "real-cap")
	artifacts := filepath.Join(reports, "artifacts")
	// The Host's own files: the segment ledger and summary, and the report
	// capability under this launch's report directory.
	writeFile(t, filepath.Join(segment, "launch.json"), `{"phase":"exited","cause_code":"real-cause","executable":"/usr/bin/sandbox-exec"}`)
	writeFile(t, filepath.Join(segment, "provider-diagnostics.json"), `{"captured_bytes":10}`)
	writeFile(t, filepath.Join(reports, "capability.json"), `{"token":"report-token-deadbeef"}`)
	// Provider-writable lookalikes with the same basenames.
	writeFile(t, filepath.Join(scratch, "launch.json"), `{"phase":"FAKE_SCRATCH_PHASE","executable":"/bin/fake"}`)
	writeFile(t, filepath.Join(scratch, "provider-diagnostics.json"), `{"captured_bytes":999}`)
	writeFile(t, filepath.Join(scratch, "capability.json"), `{"token":"report-token-fake"}`)
	writeFile(t, filepath.Join(artifacts, "launch.json"), `{"phase":"FAKE_ARTIFACT_PHASE"}`)
	writeFile(t, filepath.Join(artifacts, "provider-diagnostics.json"), `{"captured_bytes":888}`)
	writeFile(t, filepath.Join(artifacts, "capability.json"), `{"token":"report-token-fake2"}`)

	diagnostic := matrixDiagnose(state)
	if diagnostic.LaunchPhase != "exited" || diagnostic.LaunchCauseCode != "real-cause" {
		t.Errorf("launch ledger taken from a lookalike: phase=%q cause=%q ambiguous=%v", diagnostic.LaunchPhase, diagnostic.LaunchCauseCode, diagnostic.AmbiguousSources)
	}
	if got := diagnostic.ProviderDiagnostics["captured_bytes"]; got != float64(10) {
		t.Errorf("provider diagnostics taken from a lookalike: captured_bytes=%v ambiguous=%v", got, diagnostic.AmbiguousSources)
	}
	if !diagnostic.ReportCapabilityPresent {
		t.Error("the launch report capability was lost")
	}
	if diagnostic.CapabilityPathCheck != "pass" {
		t.Errorf("capability path check = %q (rel %q), want pass for the launch capability", diagnostic.CapabilityPathCheck, diagnostic.CapabilityRel)
	}
	if len(diagnostic.AmbiguousSources) != 0 {
		t.Errorf("provider-writable lookalikes made the diagnosis ambiguous: %v", diagnostic.AmbiguousSources)
	}

	// With only lookalikes present there is no Host metadata to report.
	bare := filepath.Join(t.TempDir(), "state")
	bareScratch := filepath.Join(bare, "host-spool", "launch", "attempt", "segment", "scratch")
	writeFile(t, filepath.Join(bareScratch, "launch.json"), `{"phase":"FAKE_SCRATCH_PHASE"}`)
	writeFile(t, filepath.Join(bareScratch, "provider-diagnostics.json"), `{"captured_bytes":999}`)
	writeFile(t, filepath.Join(bareScratch, "capability.json"), `{"token":"report-token-fake"}`)
	bareDiagnostic := matrixDiagnose(bare)
	if bareDiagnostic.LaunchPhase != "" || bareDiagnostic.ProviderDiagnostics != nil || bareDiagnostic.ReportCapabilityPresent {
		t.Errorf("lookalike-only tree produced diagnostics: phase=%q provider=%v capability=%t", bareDiagnostic.LaunchPhase, bareDiagnostic.ProviderDiagnostics, bareDiagnostic.ReportCapabilityPresent)
	}
	if bareDiagnostic.CapabilityPathCheck != "not_evaluated" {
		t.Errorf("capability check = %q, want not_evaluated without a launch capability", bareDiagnostic.CapabilityPathCheck)
	}
}

// The diagnostic recomputation has to follow the production guard in
// sandboxCommand: resolve the capability path, compare it with the resolved
// report directory, and keep the capability id across resolution.
func TestMatrixDiagnoseCapabilityCheckFollowsProductionGuard(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	ownDir := filepath.Join(state, "host-spool", "launch", ".reports", "own")
	if err = os.MkdirAll(ownDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ownDir, "capability.json"), []byte(`{"token":"report-token-deadbeef"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := matrixDiagnose(state).CapabilityPathCheck; got != "pass" {
		t.Errorf("canonical capability check = %q, want pass", got)
	}
	// The same tree reached through a symlinked state prefix is the launch the
	// production guard now accepts; the diagnostic must not reject it.
	link := filepath.Join(base, "state-link")
	if err = os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	if got := matrixDiagnose(link).CapabilityPathCheck; got != "pass" {
		t.Errorf("symlinked-prefix capability check = %q, want pass", got)
	}
	// A capability redirected outside the tree is the only one the walk can
	// see, and the check itself has to reject it.
	redirected := filepath.Join(t.TempDir(), "state")
	redirectedDir := filepath.Join(redirected, "host-spool", "launch", ".reports", "own")
	// The target lives outside this state tree, so the redirected file is the
	// only capability the walk can see and the check itself has to reject it.
	redirectedVictim := filepath.Join(t.TempDir(), "victim", "capability.json")
	if err = os.MkdirAll(filepath.Dir(redirectedVictim), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(redirectedVictim, []byte(`{"token":"victim-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(redirectedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(redirectedVictim, filepath.Join(redirectedDir, "capability.json")); err != nil {
		t.Fatal(err)
	}
	if got := matrixDiagnose(redirected).CapabilityPathCheck; got != "reject" {
		t.Errorf("external capability check = %q, want reject", got)
	}
	// The id itself must survive resolution. A sibling target inside the same
	// report directory resolves two elements deep, so only the id comparison
	// rejects it; at the whole-tree level the second id instead makes the
	// source ambiguous, which the sole-path rule already refuses to pick.
	sibling := filepath.Join(t.TempDir(), "state")
	siblingVictim := filepath.Join(sibling, "host-spool", "launch", ".reports", "victim", "capability.json")
	siblingOwn := filepath.Join(sibling, "host-spool", "launch", ".reports", "own", "capability.json")
	if err = os.MkdirAll(filepath.Dir(siblingVictim), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(siblingVictim, []byte(`{"token":"victim-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(siblingOwn), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(siblingVictim, siblingOwn); err != nil {
		t.Fatal(err)
	}
	if verdict, _ := matrixCapabilityVerdict(siblingVictim); verdict != "pass" {
		t.Errorf("sibling target verdict = %q, want pass for the id's own file", verdict)
	}
	if verdict, _ := matrixCapabilityVerdict(siblingOwn); verdict != "reject" {
		t.Errorf("cross-id verdict = %q, want reject", verdict)
	}
	if got := matrixDiagnose(sibling).CapabilityPathCheck; got != "not_evaluated" {
		t.Errorf("two-id tree check = %q, want not_evaluated (ambiguous source)", got)
	}
}

func TestMatrixSummaryReportsUnsettledCleanup(t *testing.T) {
	records := []*matrixRecord{
		{Name: "agy-baseline", OK: true, Result: "read_collected_accepted", Cleanup: string(stopExited) + "; state_removed"},
		{Name: "agy-ceiling", OK: true, Result: "read_collected_accepted", Cleanup: string(stopIdentityUnknown) + ": pid 42 is running; state_preserved_in_place"},
		{Name: "grok-baseline", Result: "task_failed", Cleanup: string(stopHolderSurvives) + ": pids [43] remain; state_preserved_in_place"},
	}
	summary := matrixSummarize(records)
	// The trial verdict stays a fact about the trial and the leak is named beside it
	// and fails the round. Folding the two together would hide whether the model
	// round actually succeeded, which is the number the matrix is read for.
	if summary.RealAccepted != 2 {
		t.Errorf("real_accepted = %d, want 2", summary.RealAccepted)
	}
	if len(summary.UnsettledCleanups) != 2 {
		t.Fatalf("unsettled cleanups = %v, want the agy-ceiling and grok-baseline entries", summary.UnsettledCleanups)
	}
	joined := strings.Join(summary.UnsettledCleanups, " ")
	for _, want := range []string{"agy-ceiling", "grok-baseline"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing from %v", want, summary.UnsettledCleanups)
		}
	}
	if strings.Contains(joined, "agy-baseline") {
		t.Errorf("a settled cleanup was reported as unsettled: %v", summary.UnsettledCleanups)
	}
}

// Runs in default automation: it drives no provider and calls no model. These
// withholding rules decide what a real round may write into tracked evidence, so
// a leak has to turn this red instead of only appearing inside an opt-in matrix.
func TestMatrixEvidenceWithholdsProviderSessionAndCapabilityMaterial(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	segment := filepath.Join(root, "host-spool", "launch", "attempt", "segment")
	for _, dir := range []string{
		filepath.Join(segment, "scratch"),
		filepath.Join(root, "host-spool", "launch", ".reports", "report-capability-abc"),
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "host-spool", "launch", ".reports", "report-capability-abc", "capability.json"): `{"token":"report-token-deadbeef","expires_at":"later"}`,
		filepath.Join(segment, "scratch", "grok-session-intent.json"):                                       `{"session_id":"11111111-2222-3333-4444-555555555555","session_directory":"/Users/operator/.grok/sessions/%2Fprivate%2Ftmp%2Fwork/11111111-2222-3333-4444-555555555555","socket_directory":"/private/tmp/codex-grok-11111111"}`,
		filepath.Join(segment, "launch.json"):                                                               `{"phase":"exited","executable":"/usr/bin/sandbox-exec"}`,
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	byName := map[string]matrixSpoolEntry{}
	for _, entry := range matrixSpoolInventory(root) {
		byName[filepath.Base(entry.Path)] = entry
	}
	if got := byName["capability.json"]; got.Withheld != "report_capability_material_withheld" || got.Excerpt != "" {
		t.Fatalf("capability material not withheld: %+v", got)
	}
	if got := byName["grok-session-intent.json"]; got.Withheld != "provider_session_material_withheld" || got.Excerpt != "" {
		t.Fatalf("provider session material not withheld: %+v", got)
	}
	if got := byName["launch.json"]; got.Withheld != "" || !strings.Contains(got.Excerpt, "sandbox-exec") {
		t.Fatalf("ordinary ledger entry lost: %+v", got)
	}
	// The patterns are the backstop for material that arrives somewhere else.
	redacted := redactSecrets(`{"session_id":"11111111-2222-3333-4444-555555555555","session_directory":"/Users/operator/.grok/sessions/x","control_token":"abc123","api_key":"keyvalue"}`)
	for _, forbidden := range []string{"11111111-2222-3333-4444-555555555555", "/Users/operator/.grok/sessions", "\"abc123\"", "\"keyvalue\""} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redaction kept %q: %s", forbidden, redacted)
		}
	}
	if !strings.Contains(redacted, "REDACTED") {
		t.Fatalf("redaction produced no markers: %s", redacted)
	}
}

// A task-local explicit model is separate from the binary version and account.
// It is used for user-authorized compatible-endpoint trials, never latest lookup.
func matrixConfigureModel(profile *adapter.ExecutionProfile) error {
	model := os.Getenv("ORCHESTRATOR_MATRIX_MODEL")
	if model == "" || model == "cli-default" || !adapter.ValidModelID(model) {
		return errors.New("matrix_model_invalid")
	}
	profile.Model = adapter.ModelID(model)
	return nil
}

func TestMatrixExplicitModelDoesNotChangePermissions(t *testing.T) {
	t.Setenv("ORCHESTRATOR_MATRIX_MODEL", "deepseek-v4.1-flash-1017")
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	if err := matrixConfigureModel(&profile); err != nil || profile.Model != "deepseek-v4.1-flash-1017" || profile.Permission != adapter.ReadOnly {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	t.Setenv("ORCHESTRATOR_MATRIX_MODEL", "bad model --unsafe")
	if err := matrixConfigureModel(&profile); err == nil {
		t.Fatal("invalid model admitted")
	}
}

func TestMatrixExplicitModelRejectsMissingOrDefault(t *testing.T) {
	for _, model := range []string{"", "cli-default"} {
		t.Run(model, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_MATRIX_MODEL", model)
			if err := matrixConfigureModel(&adapter.ExecutionProfile{}); err == nil {
				t.Fatal("matrix accepted a model without an explicit identity")
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv("ORCHESTRATOR_MATRIX_MODEL", "temporary")
		if err := os.Unsetenv("ORCHESTRATOR_MATRIX_MODEL"); err != nil {
			t.Fatal(err)
		}
		if err := matrixConfigureModel(&adapter.ExecutionProfile{}); err == nil {
			t.Fatal("matrix accepted a missing model")
		}
	})
}
