# Host peer expectation — final validation

Qualified source f92478c81953d70f661ab9398a25bae3aab46b8a; Go tree
01a1c81fa61785ab59881febe633ac0f3b3d1ace.
Production 389a6fed3efd33b394862be2ca4f4d66d4a955b5.
Independent review host_expectation_review PASS at f92478c after fixing the
Capture deadline/cancellation and raw-birth test-oracle blockers.

HostPeerExpectation is only a destination comparison value: fixed domain,
current Host PID, full kernel raw birth, and effective UID. Capture requires a
finite live context and only captures self. Verify takes a caller-supplied
kernel peer PID, requires the exact expected PID, and double-observes the full
live record with context checks. RawBirth is 1..128 printable ASCII bytes
0x21..0x7e. No Handle/LiveDescendant or transform authority can be constructed
from this value. A future caller must obtain peer PID from the same Unix
connection; this leaf performs no socket authentication.

ROOT verified every command's source/Go tree/clean state, exit, raw stdout and
stderr and actual JSON test counts. All 17 command sets, including failures,
are retained in data/root-verification.json and their original sidecars.

- Initial rejecting-stub RED is real behavior failure, not missing-API compile
  evidence. The first green fixture compile error (a := redeclaration) remains
  recorded separately.
- Old Capture accepted an unbounded context; the real Capture RED stdout SHA is
  72f732adc45778a2fc04345c0063738308a777a706b522cad03b6c32c699ea45.
- Production 389a6fe final race: 6 top / 16 named passes, 0 failures/skips.
  SHA 92d35b70f94a76db1c4a30152cc3f4d29cd682b561b76b37c607041e3f3c5de3.
  Covers Capture nil/no-deadline/pre-cancelled/observer-cancelled/positive,
  Verify full-record drift, invalid input, cancellation during second read and
  actual native self.
- ROOT f92478c fixes only test oracles: 129 printable bytes isolate maximum
  length, 128 printable bytes pass both observations, C1 and invalid UTF-8
  reject with zero observations. The native negative keeps PID and changes
  RawBirth, requiring actual kernel comparison. The affected 2 top / 2 named
  race tests pass, 0 failed/skipped. SHA
  ac8abfcc7c71108989cfcb84feb1080c2899c7138f587b2bf34d2890300719c1.
  Remaining unchanged production/tests reuse the 389a6fe evidence.
- Actual Darwin self PID 50258, UID 501, raw birth
  darwin:1791626048:211775.
- Actual Linux self in the existing cached linux/arm64 image: 1 top / 1 named
  pass, 0 failed/skipped, SHA
  65a926847fdfe159ce3dda4ce8e0e2e29d87381f57099ffcc6126d54979bb42d.
  PID 1 / UID 0 / raw birth
  linux:66d3d3ca-2d45-4802-b4b1-2ef8e2ed0a2b:155.
  Image ID sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99;
  --pull=never, --network=none, --read-only, --user=0:0, bounded 8 MiB tmpfs,
  read-only test binary mount. No download or real provider.
- Final process vet, Linux test-binary compile, and worker Darwin/Linux
  process-package builds returned 0 with empty stderr. Worker build commands
  were ./internal/process, not whole-module builds. Combined whole-module
  build evidence, when performed, belongs in the integration receipt.

ROOT original sessions 38555, 4262, 12241 were drained; final vet synchronous.
Worker reports its outer calls completed synchronously without session IDs;
ROOT verified raw commands/results, not those outer tool IDs. No new native
child process was launched by the self test.

The container closure-host-expectation-f92478c is absent after original return.
The one owned Linux test binary, 5,676,271 bytes, and its empty linux-native
directory were removed after fsynced exact identity/hash intent; raw evidence
and source remain. Cleanup intent SHA
83ae750aee48339eacd4f6f1fb919debcf7c1f02cfe310182a28de299e419be9;
result SHA 20bccd94c828db44ca063d196b4743094c12a50d4504d037cf329582fe970666.

This does not implement carrier B, helper pin/routing, socket path lifecycle,
Store quota, target transition or public filtered merge. Actual Linux self
observation is not Linux Broker/filter/native Git acceptance. The current F13
package source 4e1229c predates this leaf. No user installation, real hook,
model/account, push or release was used; historical UNKNOWN roots were untouched.

