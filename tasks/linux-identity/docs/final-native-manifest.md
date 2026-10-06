# Final Linux process/IPC native verification

- Source revision before this evidence record: `2fbeeef9f11c744a7ca42c37c15a43f89a1cb570`.
- Image: `redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`.
- Environment: Linux arm64 container; `--network none --read-only --cap-drop ALL`
  `--security-opt no-new-privileges --user 65534:65534 --pids-limit 64`
  `--memory 256m --cpus 1 --tmpfs /tmp:rw,nosuid,nodev,size=128m --init`.
- Build environment: Go 1.26.8, `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`,
  `GOMAXPROCS=2`, isolated `GOCACHE`.
- Test binary SHA-256:
  - process: `1fcc4b99e25d0bcff8d14d8d6e97705a77fcca872592beb103c4cb9b971b3cd6`
  - ipc: `0fabe4f030b52778486b37fee1f926335a558f66397add69d21b0f6043e2146c`
- Exact process `-test.run` regex:
  `^(TestLinuxExecutableRejectsKernelTargetInodeMismatch|TestLinuxAncestryUsesObservedStatForBirth|TestLinuxAncestryCancellationWins|TestLinuxParseStatAcceptsSpacesAndRightParensInComm|TestLinuxParseStatRejectsMalformedOrUntrustedValues|TestLinuxOriginalAPIsSelfBirth|TestLinuxOriginalAPIsSelfExecutable|TestAncestryContextRealOSSelfAndCompatibility|TestAncestryContextRealOSParentChildAndGone|TestLinuxGroupQuiescentAndExitedForWaitedLeader)$`
- Exact IPC `-test.run` regex: `^TestLinuxOriginalAPIsPeerPIDOverUnixSocket$`.
- Both used `-test.v -test.timeout=30s`; raw output is
  `tasks/linux-identity/data/final-native.log` (ignored operational evidence).
  It records 11 named PASS tests, exit 0, CID
  `d15af0f051051d56bf72e5ba0aa4f9d68acf8e340b0fdb1a19fd9f059e2f896b`,
  and post-removal `docker inspect` returning `No such object`.

This evidence covers the Linux process/IPC milestone only. Coordinator real
UDS authorization remains open; no claim is made for Linux Provider support.
