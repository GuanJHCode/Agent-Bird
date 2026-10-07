# M1a bounded capture and chunk format

This foundation is not the detailed-diagnostics feature. No on-disk writer,
rolling slots, manifest recovery, public read/ACK, artifact admission or GC is
implemented by this increment. M1b and public M2/M3 remain required.

ROOT independently copied immutable commits into isolated temporary modules:
ce7530 failed six additional behavioral regressions (chunk4fa8d6 exit1);
0ffd882 still failed Close/offset/lease-ID boundaries (77235a exit1). a5b2cf
passed those six with race (95289 exit0), but a separate format audit
(session32086 exit1) showed all six invalid range cases accepted and 131208
allocated bytes/op for a 38092-byte,2000-record chunk. These are actual
behavioral REDs. Initial worker missing-symbol compile failures are not used
as behavioral evidence, and worker test exit summaries are not acceptance.

ROOT took sole write/test ownership after6795260, added colocated independent
regressions and first ran session64905 exit1. Encoding still accepted invalid
ranges, empty/invalid-identity chunks and Done hid a newly discovered fatal.
The final writer and iterator now share per-stream range validation, pre-check
all arithmetic/size bounds and reject empty or invalid-identity chunks. Done
releases its exact lease and preserves both append and terminal errors.

Final full-package race session99038 exited0 (24 top-level /36 named,0fail/0skip); exact counts/log hashes/source
hashes are in data/root-core-final-receipt.json. The decoder keeps a constant
metadata view and iterator, with144 allocated bytes/op in the final observation;
this is an allocation test, not a full Host RSS measurement. Capture payload
slots include queued and leased data, but borrowed Data remains read-only and
must not be retained across Done. Caller and future writer/page lifetimes must
honor that constraint. go vet and diff check passed.

Tests cover checksummed malformed frames independently from the encoder,
per-stream interleaving/gaps/overlap/overflow, live Close waiting for capacity,
final-loss errors, context cancellation while acquiring the mutex, leases and
bounded allocations. Independent final review returned PASS. The final Close test uses a
channel-observed wait boundary rather than a fixed20ms delay; final evidence
is recorded in data/root-core-final-receipt.json. No native child process, Docker, real model or installation was used
for this M1a increment. The original F07 native evidence is separate.
