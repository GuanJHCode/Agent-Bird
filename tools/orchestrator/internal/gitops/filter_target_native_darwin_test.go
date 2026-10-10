//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

type filterTargetNativeJournal struct {
	*rawFixtureJournal
	observations []FilteredTargetObservationV1
	after        func(FilteredTargetObservationV1)
	afterGit     func(GitProcessRecord)
}

func (j *filterTargetNativeJournal) RecordGitProcess(ctx context.Context, in GitProcessRecord) error {
	if err := j.rawFixtureJournal.RecordGitProcess(ctx, in); err != nil {
		return err
	}
	if j.afterGit != nil {
		j.afterGit(in)
	}
	return nil
}

func (j *filterTargetNativeJournal) RecordFilteredTargetObservation(_ context.Context, in FilteredTargetObservationV1) error {
	copy := in
	copy.OrderedInputOIDs = append([]string(nil), in.OrderedInputOIDs...)
	copy.BaseFilter = in.BaseFilter.Clone()
	j.observations = append(j.observations, copy)
	if j.after != nil {
		j.after(copy)
	}
	return nil
}

type filterTargetNativeBaseline struct {
	head, ref, index []byte
	files            map[string][]byte
	modes            map[string]os.FileMode
	identities       map[string]FileIdentity
}

func filterTargetNativeCapture(t *testing.T, repo string) filterTargetNativeBaseline {
	t.Helper()
	result := filterTargetNativeBaseline{files: map[string][]byte{}, modes: map[string]os.FileMode{}, identities: map[string]FileIdentity{}}
	for _, rel := range []string{"a.txt", "README", ".gitattributes", ".git/index", ".git/HEAD", ".git/refs/heads/main"} {
		full := filepath.Join(repo, rel)
		raw, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read preserved %s: %v", rel, err)
		}
		info, err := os.Stat(full)
		if err != nil {
			t.Fatalf("stat preserved %s: %v", rel, err)
		}
		id, err := identityFromInfo(info)
		if err != nil {
			t.Fatalf("identity preserved %s: %v", rel, err)
		}
		result.files[rel], result.modes[rel], result.identities[rel] = raw, info.Mode(), id
	}
	result.head = append([]byte(nil), result.files[".git/HEAD"]...)
	result.ref = append([]byte(nil), result.files[".git/refs/heads/main"]...)
	result.index = append([]byte(nil), result.files[".git/index"]...)
	return result
}

func filterTargetNativeAssertPreserved(t *testing.T, repo string, before filterTargetNativeBaseline) {
	t.Helper()
	after := filterTargetNativeCapture(t, repo)
	if !reflect.DeepEqual(after.files, before.files) || !reflect.DeepEqual(after.modes, before.modes) || !reflect.DeepEqual(after.identities, before.identities) || !bytes.Equal(after.head, before.head) || !bytes.Equal(after.ref, before.ref) || !bytes.Equal(after.index, before.index) {
		t.Fatalf("target changed by observation: before=%#v after=%#v", before, after)
	}
}

// filterTargetNativeAccepted produces an accepted candidate through the same
// producer materialization/writer/freeze path used by the native freeze fixture.
// Its returned receipt is the compact Host-facing candidate from CandidateArtifact.
func filterTargetNativeAccepted(t *testing.T, ctx context.Context) (*rawFixture, string, *FilterGitContext, gitfilter.ResourceBroker, string, string, CandidateReceipt, []byte, *FilterPreparedWorkspace) {
	t.Helper()
	f, repo, source, producer, _, base, request, prepared := prepareFreezeFixture(t, ctx, "producer", "producer-command", "producer-filter")
	renderedCandidate := []byte("accepted provider result R\n")
	payload := filepath.Join(f.root, "accepted-provider-rendered")
	if err := os.WriteFile(payload, renderedCandidate, 0600); err != nil {
		t.Fatal(err)
	}
	copyProviderFile(t, f, ctx, payload, filepath.Join(request.Worktree, "a.txt"))
	frozen, err := FreezeFilteredWorkspace(ctx, prepared, &freezeFixtureJournal{rawFixtureJournal: f.journal})
	if err != nil {
		t.Fatalf("freeze accepted filtered candidate: %v", err)
	}
	artifact, err := CandidateArtifact("", frozen)
	if err != nil {
		t.Fatalf("encode compact accepted candidate: %v", err)
	}
	var delivery struct {
		Candidate CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(artifact, &delivery); err != nil {
		t.Fatalf("decode compact accepted candidate: %v", err)
	}
	if delivery.Candidate.Files != nil || delivery.Candidate.AttemptID != "producer" || delivery.Candidate.CandidateOID != frozen.CandidateOID || delivery.Candidate.PrivateRef != frozen.PrivateRef {
		t.Fatalf("candidate artifact did not provide compact accepted candidate: %#v", delivery.Candidate)
	}
	canonicalBase := []byte("canonical B\n")
	if err = os.WriteFile(filepath.Join(repo, "a.txt"), xorRaw(canonicalBase), 0600); err != nil {
		t.Fatalf("render source target filtered file: %v", err)
	}
	if producer.State().Unknown {
		f.retain = true
		t.Fatal("producer broker became unknown")
	}
	return f, repo, source, producer, base, request.FilterScopeSHA256, delivery.Candidate, canonicalBase, prepared
}

func filterTargetNativeRejectBeforeGit(t *testing.T, ctx context.Context, source *FilterGitContext, repo, base, attempt, command, reservation string, accepted CandidateReceipt) {
	t.Helper()
	fixture := newRawFixture(t)
	journal := &filterTargetNativeJournal{rawFixtureJournal: &rawFixtureJournal{}}
	fixture.journal = journal.rawFixtureJournal
	broker, scope := freezeBroker(t, fixture, ctx, source.manifest, attempt, command, reservation)
	live, err := InspectFilteredTarget(ctx, source, broker, FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: attempt, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: reservation}, accepted, journal)
	if live != nil || err == nil {
		if live != nil {
			_ = live.Close()
		}
		t.Fatalf("pre-Git target guard accepted malformed target: live=%v err=%v", live, err)
	}
	if len(journal.records) != 0 || len(journal.observations) != 0 || broker.State().EverStarted {
		t.Fatalf("pre-Git target guard started observation: records=%d observations=%d broker=%+v", len(journal.records), len(journal.observations), broker.State())
	}
}

func filterTargetNativeGitDigest(source *FilterGitContext, args ...string) string {
	argv := append([]string(nil), filterGitConfigArgs(source.manifest.DriverID)...)
	argv = append(argv, "-C", source.repoRoot)
	argv = append(argv, args...)
	return DigestDetail(source.repoRoot + "\x00" + strings.Join(argv, "\x00"))
}

func TestInspectFilteredTargetNativeFixture(t *testing.T) {
	t.Run("accepted_candidate_observes_rendered_target_without_mutation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		producerFixture, repo, source, producer, base, _, accepted, canonicalBase, prepared := filterTargetNativeAccepted(t, ctx)
		defer source.Close()
		defer prepared.Close()
		before := filterTargetNativeCapture(t, repo)

		func() {
			sidecar := filepath.Join(repo, ".git", "sharedindex.fixture")
			if err := os.WriteFile(sidecar, []byte("not an index\n"), 0600); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
					t.Fatalf("remove owned split-index sidecar: %v", err)
				}
			}()
			filterTargetNativeRejectBeforeGit(t, ctx, source, repo, base, "inspect-sidecar", "inspect-sidecar-command", "inspect-sidecar-filter", accepted)
		}()

		func() {
			main := filepath.Join(repo, ".git", "refs", "heads", "main")
			original, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(main)
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(repo, ".git", "refs", "heads", "alias")
			if err = os.WriteFile(alias, []byte(base+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(main, []byte("ref: refs/heads/alias\n"), info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(main, original, info.Mode().Perm()); err != nil {
					t.Fatalf("restore main loose ref: %v", err)
				}
				if err := os.Remove(alias); err != nil && !os.IsNotExist(err) {
					t.Fatalf("remove owned loose ref alias: %v", err)
				}
			}()
			filterTargetNativeRejectBeforeGit(t, ctx, source, repo, base, "inspect-symref", "inspect-symref-command", "inspect-symref-filter", accepted)
		}()

		observerFixture := newRawFixture(t)
		journal := &filterTargetNativeJournal{rawFixtureJournal: &rawFixtureJournal{}}
		observerFixture.journal = journal.rawFixtureJournal
		observer, scope := freezeBroker(t, observerFixture, ctx, source.manifest, "inspect", "inspect-command", "inspect-filter")
		request := FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: "inspect", PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "inspect-filter"}
		live, err := InspectFilteredTarget(ctx, source, observer, request, accepted, journal)
		if err != nil || live == nil {
			t.Fatalf("inspect accepted rendered target: live=%v err=%v", live, err)
		}
		receipt := live.Receipt()
		if receipt.Version != 1 || receipt.AttemptID != request.AttemptID || receipt.TargetWorktree != repo || receipt.TargetRef != request.Ref || receipt.BaseOID != base || receipt.CandidateOID != accepted.CandidateOID || receipt.CandidateTreeOID != accepted.TreeOID || receipt.FilterScopeSHA256 != scope || receipt.FilterReservationID != request.FilterReservationID || len(receipt.BaseFilter.Views) != 1 {
			t.Fatalf("incomplete target observation receipt: %#v", receipt)
		}
		view := receipt.BaseFilter.Views[0]
		if view.Path != "a.txt" || view.CanonicalSHA256 != rawSHA(canonicalBase) || view.RenderedSHA256 != rawSHA(xorRaw(canonicalBase)) || view.CanonicalSHA256 == view.RenderedSHA256 || receipt.BaseFilter.ValidateBinding(source.manifest, source.SHA256()) != nil {
			t.Fatalf("target base filter evidence=%#v", receipt.BaseFilter)
		}
		if len(journal.observations) != 1 || !reflect.DeepEqual(journal.observations[0], receipt) {
			t.Fatalf("target journal receipt=%#v live=%#v", journal.observations, receipt)
		}
		accepted.Binding.RunID = "caller-mutated"
		accepted.OrderedInputOIDs[0] = "caller-mutated"
		accepted.Filter.Views[0].Path = "caller-mutated"
		exported := live.Receipt()
		exported.OrderedInputOIDs[0] = "returned-mutated"
		exported.BaseFilter.Views[0].Path = "returned-mutated"
		if got := live.Receipt(); !reflect.DeepEqual(got, receipt) || !reflect.DeepEqual(journal.observations[0], receipt) {
			t.Fatalf("target observation retained caller/exported mutation: live=%#v journal=%#v want=%#v", got, journal.observations[0], receipt)
		}
		if err = live.Verify(ctx); err != nil {
			t.Fatalf("verify live target observation: %v", err)
		}
		filterTargetNativeAssertPreserved(t, repo, before)

		// Capture the exact normal Verify record count. The next callback fires
		// at its final exited record, so a successful final pin check must not
		// start a follow-up Git process after the source config is changed.
		healthyStart := len(journal.records)
		if err = live.Verify(ctx); err != nil {
			t.Fatalf("healthy target verification for callback cadence: %v", err)
		}
		healthyDelta := len(journal.records) - healthyStart
		if healthyDelta < 1 || journal.records[len(journal.records)-1].Phase != "exited" {
			t.Fatalf("healthy target verification records=%d final=%#v", healthyDelta, journal.records[len(journal.records)-1])
		}
		config := filepath.Join(source.commonDir, "config")
		configBytes, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		configInfo, err := os.Stat(config)
		if err != nil {
			t.Fatal(err)
		}
		configIdentity, err := identityFromInfo(configInfo)
		if err != nil {
			t.Fatal(err)
		}
		mutatedConfig := append([]byte(nil), configBytes...)
		at := bytes.Index(mutatedConfig, []byte("fixture"))
		if at < 0 {
			t.Fatal("fixture config lacks mutable fixture value")
		}
		mutatedConfig[at] = 'F'
		configStart, configFired := len(journal.records), false
		journal.afterGit = func(record GitProcessRecord) {
			if len(journal.records)-configStart != healthyDelta {
				return
			}
			if record.Phase != "exited" {
				t.Fatalf("config callback record=%#v", record)
			}
			if err := os.WriteFile(config, mutatedConfig, configInfo.Mode().Perm()); err != nil {
				t.Fatalf("late config callback: %v", err)
			}
			configFired = true
			journal.afterGit = nil
		}
		if err = live.Verify(ctx); err == nil {
			t.Fatal("live target observation accepted late source config mutation")
		}
		if !configFired || len(journal.records)-configStart != healthyDelta {
			t.Fatalf("late config callback fired=%t records=%d want=%d", configFired, len(journal.records)-configStart, healthyDelta)
		}
		if err = os.WriteFile(config, configBytes, configInfo.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(config, configInfo.ModTime(), configInfo.ModTime()); err != nil {
			t.Fatalf("restore config mtime: %v", err)
		}
		restoredConfig, err := os.Stat(config)
		if err != nil {
			t.Fatal(err)
		}
		restoredConfigIdentity, err := identityFromInfo(restoredConfig)
		if err != nil || restoredConfigIdentity != configIdentity {
			t.Fatalf("config identity restore identity=%#v want=%#v err=%v", restoredConfigIdentity, configIdentity, err)
		}

		privateRef := filepath.Join(source.commonDir, filepath.FromSlash(accepted.PrivateRef))
		privateBytes, err := os.ReadFile(privateRef)
		if err != nil {
			t.Fatal(err)
		}
		privateInfo, err := os.Stat(privateRef)
		if err != nil {
			t.Fatal(err)
		}
		privateIdentity, err := identityFromInfo(privateInfo)
		if err != nil {
			t.Fatal(err)
		}
		privateBackup := privateRef + ".fixture-backup"
		privateDigest := filterTargetNativeGitDigest(source, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", accepted.PrivateRef)
		privateFired := false
		journal.afterGit = func(record GitProcessRecord) {
			if record.Phase != "exited" || record.CommandDigest != privateDigest || privateFired {
				return
			}
			if err := os.Rename(privateRef, privateBackup); err != nil {
				t.Fatalf("withdraw private ref after its Git query: %v", err)
			}
			privateFired = true
			journal.afterGit = nil
		}
		if err = live.Verify(ctx); err == nil {
			t.Fatal("live target observation accepted withdrawn private ref")
		}
		if !privateFired {
			t.Fatal("private ref callback did not observe exact for-each-ref exit")
		}
		if err = os.Rename(privateBackup, privateRef); err != nil {
			t.Fatalf("restore private ref identity: %v", err)
		}
		restoredPrivateBytes, err := os.ReadFile(privateRef)
		if err != nil || !bytes.Equal(restoredPrivateBytes, privateBytes) {
			t.Fatalf("private ref bytes restore bytes=%q want=%q err=%v", restoredPrivateBytes, privateBytes, err)
		}
		restoredPrivateInfo, err := os.Stat(privateRef)
		if err != nil {
			t.Fatal(err)
		}
		restoredPrivateIdentity, err := identityFromInfo(restoredPrivateInfo)
		if err != nil || restoredPrivateIdentity != privateIdentity {
			t.Fatalf("private ref identity restore identity=%#v want=%#v err=%v", restoredPrivateIdentity, privateIdentity, err)
		}
		if err = live.Verify(ctx); err != nil {
			t.Fatalf("live target observation rejected exact private ref restoration: %v", err)
		}

		// Replace the directory entry, then restore its bytes through another
		// replacement: the original observation must retain the old identity.
		readme := filepath.Join(repo, "README")
		drift := filepath.Join(repo, "README.drift")
		if err = os.WriteFile(drift, []byte("target drift\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(drift, readme); err != nil {
			t.Fatal(err)
		}
		if err = live.Verify(ctx); err == nil {
			t.Fatal("live target observation accepted renamed README drift")
		}
		if err = os.WriteFile(drift, before.files["README"], before.modes["README"].Perm()); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(drift, readme); err != nil {
			t.Fatal(err)
		}
		if err = live.Verify(ctx); err == nil {
			t.Fatal("live target observation accepted byte-restored README with replaced identity")
		}
		if err = live.Close(); err != nil {
			t.Fatalf("close target observation: %v", err)
		}
		if observer.State().Unknown || observer.Binding().AttemptID != "inspect" || producer.State().Unknown || producer.Binding().AttemptID != "producer" {
			observerFixture.retain = observer.State().Unknown
			producerFixture.retain = producer.State().Unknown
			t.Fatalf("observation close transferred broker ownership: observer=%+v producer=%+v", observer.State(), producer.State())
		}
	})

	t.Run("journal_callback_target_drift_returns_no_success_observer", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		_, repo, source, _, base, _, accepted, _, prepared := filterTargetNativeAccepted(t, ctx)
		defer source.Close()
		defer prepared.Close()
		observerFixture := newRawFixture(t)
		journal := &filterTargetNativeJournal{rawFixtureJournal: &rawFixtureJournal{}}
		journal.after = func(FilteredTargetObservationV1) {
			if err := os.WriteFile(filepath.Join(repo, "README"), []byte("journal changed tracked file\n"), 0600); err != nil {
				t.Fatalf("journal callback drift: %v", err)
			}
		}
		observerFixture.journal = journal.rawFixtureJournal
		observer, scope := freezeBroker(t, observerFixture, ctx, source.manifest, "inspect-callback", "inspect-callback-command", "inspect-callback-filter")
		live, err := InspectFilteredTarget(ctx, source, observer, FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: "inspect-callback", PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "inspect-callback-filter"}, accepted, journal)
		if live != nil || err == nil {
			if live != nil {
				_ = live.Close()
			}
			t.Fatalf("journal callback returned successful target observation: live=%v err=%v", live, err)
		}
		if len(journal.observations) != 1 {
			t.Fatalf("target observation journal calls=%d", len(journal.observations))
		}
		if observer.State().Unknown {
			observerFixture.retain = true
			t.Fatal("callback observer broker became unknown")
		}
	})
}
