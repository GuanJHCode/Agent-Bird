# Controlled cross-provider composition evidence

`TestOrderedComposeCrossProviderHostDeliveryChain` uses private copies of the
test binary as a Claude producer (A), an AGY producer (B), and a separate
Claude reviewer. It runs the real Host, Git worktree composition, validation,
review, and integration paths, with no model, account, or external provider.

The test proves the B,A source order; each manifest source's provider-lock and
adapter-payload SHA-256 values are derived from its accepted input; the review
uses its own lock; and downstream receipts retain the same composed commit.

The raw JSON test stream, stderr, and exit status are in `../data/`.

The earlier Grok fixture investigation failed at the current production
`TaskCompletionV1` admission boundary. Its same-name raw output was overwritten
by the final passing run before it was retained; no failure output is recreated
or represented as evidence here.

Grok implementation is intentionally not claimed: the current production
`TaskCompletionV1` admission path accepts Claude and AGY only. Enabling Grok
there would be a production capability/security change outside this fixture
acceptance task.
