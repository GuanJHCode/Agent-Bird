# Linux group observation boundary

Tests-only product snapshot: `9a38e71`, based on integrated `e4ea15e`. Existing Linux observation code and all Provider/Start/plugin gates are unchanged.

ROOT replaced the initial incomplete fixture before execution: both children explicitly join the intended process group; each successful Start immediately owns cleanup; handshake/output and original Wait are bounded. The explicit output pipe is independent of Cmd.Wait, so its reader is not forcibly closed by Wait to manufacture EOF.

The fixed Linux arm64 binary SHA256 is `4ba4d80d17eb674520d52d1c8f1ecef7bc368c8182baa8febbd5412f89548523`. Docker used the pinned existing image, no network, non-root uid65534, read-only filesystem and binary mount, no capabilities, bounded resources, and an owned CID. See `../data/root-native-receipt.json` for the exact command and hashes.

Two native top-level tests PASS, zero FAIL/SKIP:

- Leader gone/member live: the parent creates and owns both original Cmds in the same PGID. It releases and Waits the leader (exit0), confirms the same-birth member is live, observes nonquiescence and UNKNOWN exit, then releases and Waits the member (exit0). Only then is the group confirmed absent.
- Zombie: the original leader is released but not Waited; `/proc` proves state Z and the same birth/group identity. Quiescence is true, but exit remains UNKNOWN. The original Cmd.Wait reaps it with exit0; the group is then absent.

All three original processes reached natural stdout/stderr EOF and needed no forced cleanup. The original container CID `8e5b074c3934f7eba07d4e8590f6102037a1f7f0b7318b48fdc695d746411a75` was confirmed absent. Raw log SHA256: `98d729e4f9bfba5c04bd74defe2a1c117af2c2469b91059eaf1f1c26137a3b6e`.

Go1.26.8 Linux cross compilation and Linux `go vet ./internal/process` exit0. Darwin process-test compilation exit0 is compilation evidence only. These tests establish Linux kernel group-observation semantics; they do not establish Linux Provider execution, sandboxing, installation, full orchestration, or Intel/Windows native support.

Temporary cleanup: removed the superseded worker-built binary under the misplaced `tools/orchestrator/tasks/linux-group-native/tmp` and its empty directories. The final tested binary, raw evidence, and reproduction script are retained under the task directory.

Independent review of 9a38e71: PASS. ROOT also reran the compile/vet checks with `GOOS`, `GOARCH`, `CGO_ENABLED`, and `GOMAXPROCS` recorded explicitly in command arrays; see `compile-vet-explicit-receipt.json`. Both exit0. No native test rerun was needed because the source and tested binary were unchanged.

Linux race instrumentation was not run: this environment uses a CGO-disabled cross-compiled test binary and the pinned runtime image contains no Go/C compiler. No Linux race result is claimed. The tests-only process source is unchanged by the Task5 integration; its native evidence is reused rather than counted again.
