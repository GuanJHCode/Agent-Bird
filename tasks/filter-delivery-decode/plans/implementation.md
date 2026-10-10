# Candidate delivery strict decode

Add a Host-local decoder that delegates recursive field validation to
`contract.DecodeBridgeStrict`. It preserves optional legacy fields, including a
missing or null `candidate.filter`; filter-to-live-source binding remains a
later Host concern. Tests cover a legacy delivery, a valid filter attachment,
and strict scanning through the candidate and filter nesting boundaries.
