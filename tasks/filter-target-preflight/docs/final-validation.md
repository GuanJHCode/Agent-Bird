# Filtered target read-only primitive — final validation

Qualified source `8e29b399caaaaa875146db9fb81c391ccbb4d4e3`; production `5feb471a4fb79b1b1b32f1aed77c24b850b2b592`. This is a private original-live observer, not a public target action or integration authorization. Full evidence is in [ROOT verification](../data/root-verification.json) and immutable command/stdout/stderr/exit files alongside it.

`InspectFilteredTarget` binds authenticated compact accepted candidate provenance to the original source/Broker/journal, exact base/target branch, bounded index and complete rendered worktree inventory. The observer pins direct target/candidate refs and packed resolution, original source/config/tool/attributes/repository files, and held/named target entries. It rechecks after journal callbacks without introducing a final Git callback. Close releases only its own descriptors. Public filtered target/integrate gates remain closed.

## Results

| Check | Fixed source | Result |
|---|---|---|
| Focused pure guards, race | 5feb471 | 4 top / 25 named PASS, 0 fail/skip |
| macOS whole build | 5feb471 | exit 0, empty output |
| Linux arm64 CGO0 whole cross-build | 5feb471 | exit 0, empty output; not native qualification |
| gitops/gitopsworker vet | 5feb471 | exit 0, empty output |
| Actual native fixture, race | 8e29b39 | 1 top / 2 sub / 3 named PASS, 360.65s, 0 fail/skip |
| Native fixture vet | 8e29b39 | exit 0, empty output |

Native raw SHA256: `8e61fe42d0a269f4356c694e34c704b148ef53a13fd3cae7d91db47c7a86f563`. Pure guard raw SHA256: `f12bd7a380b1a2452ae4283bfc810476352cedb2988cb39966a1e27343370175`. All commands used the pinned Go 1.26.8 toolchain, clean recorded source and offline environment. The only changes after 5feb471 are the Darwin native fixture and its compile note, so production build and pure guard evidence is reused. Independent production and native-oracle reviews both passed.

The actual fixture runs Prepare → original controlled writer Wait/tree → Freeze → compact accepted candidate → fresh inspection Broker. Canonical B and rendered R differ. Preflight preserves every tracked target file plus raw index/HEAD/main ref bytes, modes and identities. It proves zero Git/tool starts for split-index sidecar and target symref refusal, input/exported/journal deep-copy isolation, rejection after the last Git callback changes source config (with no subsequent Git), rejection when the candidate private ref is withdrawn after its exact query, and rejection of changed or same-byte/new-inode README. Exact fixture restoration of config bytes/mtime or the original private-ref inode is explicitly only test setup, not a promise to detect transient changes restored between observations. A separate result-journal callback that writes README prevents return of a success observer.

Both controlled writers and actor builders used original Wait/tree proof. Six fixture roots were removed by their original owners and ROOT checked all six paths absent. Producer journals each contain 1470 completed original Git sequences; observers contain 1807 and 565, and the two front-door rejection journals contain zero. Each producer setup has 13 independently observed Git Wait/tree successes. Four Brokers started and terminated known; two rejection Brokers never started. No UNKNOWN root or unresolved process remains from this run.

## Corrections and evidence limits

Review of 21d659c found five blockers: candidate verification before index bounds; target symref chain; nested directory lacked named re-open; missing final candidate/source pins; case-sensitive metadata names on macOS. All were fixed in 5feb471. Old production was genuinely reproduced on clean 4eab0c9: nested rename and symlink replacement were accepted, giving 1 top / 2 sub / 3 named FAIL, exit 1, raw SHA256 `56f083c8faa91665397d9f443ea19bf0164ea09ab237a278bd176c2ab3fd0cf3`. That failure is retained, not rewritten as passing. Other guards are final regression coverage, not separately claimed historical REDs.

Worker native drafts 65576b7/cda4862/23a50d8 were only compiled; they were never native-qualified alone. ROOT moved positive baseline capture after the fixture's negative ref setup and added deferred Close before the single final native race. Existing F10/F12 crash, producer/validator/read-only-review and earlier filter primitive evidence is unchanged and not rerun.

Packed-only target success and forced UNKNOWN cleanup were not newly executed. The native target immutability oracle does not snapshot every object/reflog file under .git. No real model/account, user installation, push or release occurred. Formal60 remains FAILED, Intel Mac/Windows native remain deferred, and F13 source614 package does not cover this increment. Normal final integration must still use the original tracked git merge with required hooks/signing; this stage has no target mutation or deletion authority.
