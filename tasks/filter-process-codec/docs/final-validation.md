# Private filter.process codec validation

Current source `d5f8326965127757dd2b8b37a1705c66a9351992`, Go tree
`02b248e2bf3ed002d736ab3ec697fbc1f0c3b782`. Independent final review of d5f8326 PASS. This implements only the private
bounded protocol codec; integration receipt is recorded separately.

ROOT took over c0adead after finding missing handshake flushing, sticky producer
failure, negotiation, output packetization, cancellation and request/body bounds.
The worker's 86f3e9d `focused-race-20261010.json` is a manually written summary
of one golden test without original command/raw receipt. It is retained as
history but is not qualification evidence.

Actual clean-source evidence in `../data/`, produced by the task runner and
independently parsed by ROOT:

| Command label | Source | Result |
|---|---|---|
| codec-boundaries-red | 7e25758 | 1 failed top, 10 failed subtests, 2 passed subtests; exit 1 |
| codec-focused-race | a5261a8 | 3 top / 41 named PASS, 0 FAIL/SKIP; exit 0 |
| codec-cancel-red | 30dd8e7 | 1 failed top / 2 failed subtests; exit 1 |
| codec-final-race | d5f8326 | 4 top / 44 named PASS, 0 FAIL/SKIP; exit 0 |
| codec-final-macos-build | d5f8326 | whole-module build, exit 0 |
| codec-final-linux-build | d5f8326 | CGO=0 Linux arm64 whole-module cross-build, exit 0 |
| codec-final-vet | d5f8326 | internal/gitfilter, exit 0 |

All final stderr streams are empty. Final race stdout SHA-256:
`0fd3a1de4158669b4a13097ec0a6e598b40b12249f652675face2dcaaadff068`.
Original ROOT exec sessions 5392, 10434, 16799, 38851, 34814, 51460, 11464,
57938 all reached terminal tool results and were drained; vet completed inline.
No detached process, native actor, cleanup receipt or old long native test is
involved in this codec-only evidence.

The a5261a8 review was BLOCKED despite green tests: cancellation that closes a
blocked reader could become clean EOF, losing the terminal failure. The second
real RED reproduces both reader EOF and blocking writer completion. d5f8326
checks cancellation after actual IO as well as before it; context error wins.
All old failing evidence is preserved, not replaced by the later green output.

Verified boundaries include interactive version reply before capabilities;
strict pkt-line/metadata/EOF shapes; selected clean/smudge only; optional
ref/treeish/blob; input 16 MiB, 65536 packets, 1024 requests; one-use producer,
ignored errors, short writes and revocation; bounded current-body drain;
no response before flush; split output; cancellation after blocking IO.

Remaining: actual Git helper/Unix carrier, bidirectional peer proof, durable
carrier quota/journal, target before-to-after owner and final normal merge.
The caller must interrupt blocking IO and retain failure across helper restarts.
No real model, user installation, hook registration, push or release was used.
