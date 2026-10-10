# F04 milestone1 strict shared DTO validation

Production/test source: a793567, base e662aaa0e2a98f65ad16f30871dc8f03ce73e3a1. Initial tests/fail-closed scaffold: 4efe02f. Only new internal/contract/git_filter.go and git_filter_test.go affect Go code. No existing decoder, DTO, adapter, LaunchCommand/Event, Store, Host or public acceptance changed. ROOT owns independent review, broader build/vet and integration.

## Delivered contract

- Manifest and all nested pins reject unknown/case/duplicate JSON keys, every missing/null required field, and null array elements. Dedicated Grant/Cleanup decoders do likewise; RemovedBytes=0 is valid only when explicitly supplied.
- Manifest paths are sorted, unique, 1..256, bounded UTF-8 clean relative paths; .git components, case aliases and traversal are refused. Attributes require exact .gitattributes basename and cannot overlap writable paths. Native tool proof is not inferred from executable path shape.
- Clean/Smudge Args have independent 16-entry/4096-byte limits. Canonical manifest JSON plus its storage newline is bounded to32768 bytes; Digest hashes canonical JSON without the newline. Clone copies all slices, preserving nil versus explicit empty arrays.
- Grant fixes two32MiB tools and three16MiB streams (112MiB peak). Cleanup repeats identity fields plus journal digest and removal count. Scope is SHA256(domain git-filter-resource-v1\0 + canonical JSON {Version,HostScope,PlanRevision,ManifestSHA256}); HostScope uses the existing logical segment scope. Neither digest nor DTO validity authenticates authority or proves physical Host identity.

## Actual focused evidence

Pinned Go1.26.8, TMPDIR=/private/tmp. The recorded commands use the absolute pinned binary with -C tools/orchestrator test -json -count=1 ./internal/contract -run '^TestGitFilter'; final adds -race. command.json records exact argv/cwd/HEAD/clean status/Go version. Actual counts include parent tests and subtests, exclude package-level events; summary.json includes the exact named results.

| Batch | Source | Top / named | PASS / FAIL / SKIP | Exit | stdout SHA256 |
|---|---|---|---|---|---|
| contract-red | 4efe02f, clean | 5 /45 |31 /14 /0 |1 |521a3a827c1f1d7683a9db5700ae6e32aa9d8bdd21d191b6a66e2921b445f153 |
| contract-final-race | a793567, clean |5 /46 |46 /0 /0 |0 |fa3ca78d86c0790ddfa3ca6c4969bbcc8e2624d951bbe95aa38373b9c8cef9a5 |

Both stderr files are empty. RED failures are retained and prove the scaffold rejects valid manifests, nested pins, grants, explicit-zero cleanup and digest/scope behavior; negative cases already passed fail-closed. Final adds one named accepted-limit group plus clone checks, then implements the unchanged business expectations. Tests cover each DTO field's omission/null/duplicate/case behavior, unknown keys, nested null Args, independent limits, paths/attributes restrictions, exact32768-byte newline bound versus1-byte excess, canonical digest oracle, full slice clone and scope domain/plan/epoch semantics. No full Store or whole-module test run was performed.

## Scope and handback

This milestone is pure contract only. No reservation, quota refund, physical identity, filesystem, native filter execution, original Wait, crash recovery, dual Git views or public filter support is claimed. Schema/resource/Host wiring and authentication belong to the following milestones. No native processes or task temporary binaries were created; only runner test/build processes completed normally. All raw data and source receipts are retained; no temporary task files required deletion. No models/providers/account operations, installation replacement, push or release occurred. Formal resource qualification and Intel/Windows native acceptance remain outside this milestone.
