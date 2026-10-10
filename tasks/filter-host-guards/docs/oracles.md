# Filter Host guard test oracles

`ValidateGitFilterPipelinePayload` admits only a fresh producer shape and a target-free standalone validator shape. The test keeps one valid DTO for each, then changes exactly one authority-bearing field: target, rework, session, non-canonical directory, provider pin, or case-folding path alias.

`OpenFreshJournal` must create the first ledger only. A pre-existing empty, partial, or `filter_materialized` ledger is cold state, must be rejected, and must remain byte-identical. Journal reads retain legacy absent `filter` attachments. A duplicate direct `filter` field in the candidate receipt is rejected by strict decoding, so JSON overwrite semantics cannot add a second attachment.
