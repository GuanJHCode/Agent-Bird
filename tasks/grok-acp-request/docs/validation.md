# ACP private request reader

ROOT replaced the incomplete worker WIP with strict v1 decoding and private-file validation. Ten required exact keys, duplicate/null/alias/trailing rejection, bounded UTF-8 and paired UTF-16 escapes, timeout/profile/auth limits, current-UID single-link 0600 regular file, canonical path and before/open/read/after identity checks. Raw prompt whitespace is preserved. Errors contain no payload or path.

Behavioral RED on the old WIP proved duplicate/alias/invalid-Unicode, hardlink/parent-symlink and path/version validation failures. ROOT final race and source hashes are in `../data/root-receipt.json`; `go vet ./internal/grokorigin` exited 0. Independent read-only reliability review passed. Tests use owned temporary files only, with no binary execution or authentication access.

This is input validation only. Actual binary digest/version/workspace preflight, sandbox launch, Coordinator RPCs and public ACP flow are still required. Native Windows is not supported by this reader and has not been tested.
