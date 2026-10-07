# Diagnostics consent contract foundation

The new contract types enforce explicit version1 summary/detailed consent,
strict JSON decoding, canonical policy SHA256 and a path-free reservation ID.
Validation proves format/self-consistency only; signed launch and Store scope,
class and quota admission are required at integration. No public field or raw
capture path is enabled by this change.

The initial test failed to compile because the new API types were absent
(chunk88d0e7 exit1); this is compile RED, not a native product regression.
Final whole-contract race test session99905 exited0: 32 top-level /243 named
PASS,0FAIL,0SKIP. Exact command/log SHA is in data/root-policy-receipt.json.
Independent read-only review PASS; its grant JSON decode suggestions were added
before that final run. No schema19 migration, Store quota/read receipt, Host
capture, CLI or artifact retention completion is claimed.
