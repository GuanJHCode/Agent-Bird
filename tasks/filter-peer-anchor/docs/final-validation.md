# Original Handle peer binding validation

Qualified source `1625a0db4e523d13e1835ad1ae04088e273ac4f4`, production
`9f4abdd0309c689577155ec9a5eb1f6b35b5707e`, Go tree
`0375a3231d9239dbef4f5f84f0908b28a93d862b`. Independent final review PASS.

The original Start captures a private kernel-only full birth/euid/PGID anchor.
Failure to capture it does not change legacy Start/Wait behavior, but cannot
supply new peer authority. The original self pointer rejects copied Handles.
Stop revokes before signaling, including a failed Stop. Each check double reads
the complete bounded ancestry and checks original Wait/Stop state afterward.
BindLiveDescendant captures one exact peer tuple; Verify cannot adopt a reused
PID. Darwin/Linux require known live kernel states and stable identities/UID;
normal scheduling-state changes do not falsely alter identity. No ps fallback,
public Identity reconstruction, serialized adoption or new public merge action.

| Final check, all clean 1625 | Actual result |
|---|---|
| root-peer-final-race (macOS arm64) | 13 top / 25 named PASS, 0 FAIL/SKIP |
| root-peer-linux-native (Linux arm64, root UID 0) | 13 top / 25 named PASS, 0 FAIL/SKIP |
| root-peer-macos-build | whole-module build exit 0 |
| root-peer-linux-build | CGO=0 whole-module build exit 0 |
| root-peer-vet | process package exit 0 |

All final stderr streams empty. Mac stdout SHA-256
`c39d2a0c314f84adbb57376770b40f5fc9b3ff71c7403db7a393f376ceb473b3`;
Linux stdout SHA-256
`092b5532f770bfa33132305337b6f26c12e670e9cefd34f319af268fc23c2b00`.
ROOT parsed actual JSON actions and original process proof logs. Mac original
leader/peer 35645/35646 and Linux 27/33 both had live binding, original Wait,
exit zero, tree exit and revoked post-terminal binding. Actual waitable zombie
records were rejected by the private peer observer on both kernels. Existing
birth-failure FD accounting and original Stop negative controls also passed.

Linux used existing image digest
`sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`,
no pull/network, read-only container plus bounded tmpfs, explicit UID 0 and only
the cross-built test binary mounted read-only. The tag lookup failed; lookup by
existing immutable image ID succeeded. Go test2json ran on macOS as the original
Docker command wrapper; the test binary itself ran on the Linux kernel.
Container `closure-peer-native-1625a0d` was absent after the original command
returned. The one test binary was hashed, intent recorded, identity rechecked
and removed; raw output/source/evidence remain.

Original ROOT exec sessions 49625, 44016, 82459, 81841 and 49462 were all drained;
vet completed inline. Worker prior commands returned synchronously according to
its handoff; their runner raw/exit/metadata are retained and ROOT verified them.
Initial missing-API compile failures are NOT behavior RED. The rejecting Bind
stub has actual behavior RED. Earlier one-second race fixture failures and
reproduction remain in data and are not qualification; only the fixture timeout
was increased. Earlier drift/reparent tests returned the wrong first PID and
were invalid oracles; corrected tests require intended mutation plus exact read
counts. ROOT also removed UID 501 assumptions, covered terminal races/zero
binding, and verified shallow-copy rejection without disabling vet.

Remaining: Unix connection credential binding, bidirectional Host authentication,
original-Git gate, durable carrier quota, target transition and actual final merge.
A peer observation alone never proves transport ownership or process-tree exit.
