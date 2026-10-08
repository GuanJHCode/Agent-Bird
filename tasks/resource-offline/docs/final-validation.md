# Offline pending-delivery acceptance

PASS on macOS arm64 for frozen release candidate
`41663f7ce224494bc529f0695540cca9a5591bd8d92df5c7557ee29143d49819`
(source `51b8e3a`). Runner `efb64f7` was independently statically reviewed;
ROOT verified every raw sample and recomputed the final values.

- 301 samples spanning 300.064 seconds; peak tool RSS 35.078125 MiB (limit64).
- Mean CPU 0.48% of one core (limit1%); no kqueue fork/exit events.
- Exact run/task/attempt/segment/running/exited process tuple and pending result
  remain unchanged; the worker completed with durable Exited0 and was reaped.
- Live Owner/Host/worker enrollment preceded the private gate release. After
  worker completion and controlled origin exit, Owner/Host exact identities were
  absent. They may retire before origin exit; no new original-Wait claim is made
  for those actors. Controller and Coordinator original Popen Wait both0.
- No forced signals, cleanup errors or remaining actors; private executable and
  helper pins are unchanged. Full root verification is in the accompanying
  `formal-gated-20261008/root-verification.json`.

The first native attempt failed before measurement because the short echo
worker let Owner retire before live enrollment. Its failed summary remains in
`formal-20261008`; both original controller/Coordinator Wait0 and empty cleanup
inventory were confirmed. It is not counted as a resource pass.

This is controlled fake-worker evidence, not real Provider/model qualification.
It does not cover idle30s, four-source-Host capacity, or the separate 60-minute
flood acceptance. No installed binary/account was replaced or used.
