# DAG dependency evidence — first milestone

This milestone binds all direct prerequisites of a candidate action. The action still selects exactly one content source; other inputs are accepted evidence, not merged candidate content.

## Public contract

Set `candidate_action.dependency_input_version` to `1` on a new action with multiple dependencies. All direct dependencies must use owner review (the default). Unversioned joins and automatic artifact/fixture completion are refused at plan admission. Single-source version 0 keeps the historical launch intent bytes.

The grant carries `dependency_inputs: {version: 1, inputs: [...]}` in task-ID order. Every entry contains the accepted Host event, artifact reference, source adapter payload, and owner acceptance command. `input` must equal the entry named by `source_task`.

Schema16 stores the canonical vector and SHA256 once on `segment_runtime`. A claim verifies every predecessor is owner-accepted and exited at its current revision. Pending grant replay loads the stored capture, verifies exact dependency membership and current tuples, and rejects drift. It does not silently recapture new evidence. Execution epoch remains transport identity.

Review retry copies the vector from the original decision's `source_segment_id`, and the immutable decision binds the complete vector hash. A later retry cannot keep just the content source while replacing other evidence. Existing continuation gates remain in force. Existing linear rework explicitly refuses branched joins; transitive-closure rework is a later milestone.

The complete capture footprint (`16 + JSON UTF-8 bytes + digest bytes`) passes quota/disk admission in the claim transaction. Run/global usage triggers account it, replay adds no charge, retention deletion refunds its bytes. A capture, quota, decision, or 64 KiB frame failure rolls back the entire claim.

The public invocation path, Host grant ledger, execution entry, and candidate artifact consumer validate vector shape, source equality and launch intent before their side effects. Provider permissions and Codex guards do not change.

## Verification and review

- Product commits: 6535881 + f0eb734. No Provider permission or public Codex guard changed.
- Final ordinary regression: 89 top-level / 232 named PASS, zero FAIL/SKIP; raw SHA256 `18b0288678166402b9ae116eba723ed4d8c108ad9e1cfbbc9c6751e17b0ccadc` (`normal-final2.jsonl`).
- Original stable product race: 88 top-level / 228 named PASS, zero FAIL/SKIP; raw SHA256 `4536138b902635ef0263ea1927585e97b3840eb6a3f4d289dcbbbefd9403a327`. After the final guard/admission fixes, affected race: 15 top-level / 66 named PASS, zero FAIL/SKIP; raw SHA256 `b3ef9c2363d19fc14e5c6c55c2acaca0c569fe9c91c0102da8a6c387d9c64bdc`. These are separate runs, not additive independent scenarios.
- Final `go build ./...` and `go vet ./...` both exit0 from the Go module; exact manifests and raw logs are in `../data/`. An earlier command invoked from the repository root failed to locate the module; the module-local final commands above are the valid build evidence.
- Independent Store and Host reviews of 6535881 found duplicate-dependency admission and non-source artifact/event hash gaps. Both are fixed and independently re-reviewed PASS at f0eb734. ROOT also added null/array source-payload rejection.
- Original behavioral RED logs are retained: inherited implementation gaps, early Host/CLI boundaries, review findings, and malformed adapter objects. No failed log was replaced with a passing log.

Tests use actual SQLite transactions and controlled files. They prove capture/replay/retry/quota/migration/consumer boundaries, not live Provider execution or complete F03. Branched closure rework, cross-Provider combination, and final target serialization remain later milestones. Intel Mac/Windows native verification remains unavailable by the user's current environment constraint.
