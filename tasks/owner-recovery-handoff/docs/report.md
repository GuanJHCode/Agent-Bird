# Owner recovery handoff 文件层交接

文件层实现完成并冻结；ROOT 回报独立 FS 审查 PASS、无 blocker。工作区 `tmp/owner-recovery-handoff`，branch `feat/owner-recovery-handoff-20261008`，初始 base `a6ecc08`。先决 ROOT test-only 解环补丁 `18f3bcb` 已授权 cherry-pick 为本分支 `7731f21`；本任务代码提交仅新增 `internal/host/owner_recovery_handoff.go` / `_test.go` 与本目录 artifacts，不修改原 lease/state/runtime/Store/contract。

## 文件契约

- 冻结 `OwnerRecoveryHandoffProof` / `Facts` 和 Open/Facts/CommitPrepared/Close API，所有导出 proof/facts 字段 `json:"-"`。内部 Go proof 由 Coordinator 映射 Store 已持久 intent/applied；此 API 不建立或声称 DB 证明，公共 caller 不经此函数提供 path/old owner/AuthID。
- prior/next 同 run、launch、producer（producer 对应 launch）、相邻 incarnation；复用原 `originalOwnerRecoveryScope` 推导两个 hash。允许 Store 已确定的 origin/generation 演化。full-record digest 是整段实际 canonical JSON + 终止 newline 的 SHA256，小写 hex。
- Open existing-only directories/lock/ledger，无 O_CREAT 修补缺失权威。复用原私有 FD/NOFOLLOW/named inode、0700/0600/euid/nlink1、flock、AuthoritySHA、strict canonical record 和 sticky ledger bytes/mtime 校验。任意 OwnerPending 或 coordinator starting 拒绝；nil/reserved coordinator 可保留。
- nil proof 只接受 exact prior；prepared proof 必须同时匹配 Scope/Authority 与仅 Scope 正反向 clone 的两个 full-record hashes，只接受 exact prior/next；applied proof 只接受 exact next，prior rollback 拒绝。其他合法 budget bytes 也不能冒充 intent 对应原记录。Open/Facts 不写文件。
- CommitPrepared 只接受 exact Facts proof；prior + prepared 执行专用 scope CAS，不复用明确拒 scope change 的普通 commit；next 只读 replay。Version/Authority/Attempts/NextAttempt/Unavailable/reservation 全部保持。创建 exclusive owner-only stage、write + fsync、重验原 lease/stage、rename、重验已发布 FD、目录 fsync、named-file/readback 核验，然后接管已验证 stage FD。
- 不确定 write/rename/fsync/校验失败 sticky reject；不删 stage/ledger、不恢复旧 bytes、不 prefix GC。Close 只释放本 lease FD/flock。stage 可能不完整或被篡改，只是现场，不被当作 authority。

ROOT 裁定：原 persistBytes 的错误清理不适用于此层，窄专用 publication 仅在新文件内；ledger bytes/mtime 沿用既有规则，lock 不额外绑定内容/mtime。私有 per-lease publicationHook 无产品 flag/env 入口。

## TDD 与普通验证

标准 `go test ./internal/host` 始终保留原 fixtures；未使用 file-list 或排除测试。初始两次 baseline setup 因 native internal test → Coordinator → Host 的 import cycle 失败，未进入 tests，不算 RED；ROOT 独立解环并审查后才恢复 TDD。一轮 API 桩缺私有 hook 的编译纠正日志也单独保留。

| JSON 证据 | package P/F/S | top P/F/S | named P/F/S | 分类 |
| --- | --- | --- | --- | --- |
| baseline-setup-1 / -2 | 各 0/1/0 | 各 0/0/0 | 各 0/0/0 | 基线 setup error |
| scaffold-compile-error | 0/1/0 | 0/0/0 | 0/0/0 | 桩编译纠正，不算行为 RED |
| api-red | 0/1/0 | 0/0/0 | 0/0/0 | API undefined/build-fail |
| behavior-red | 0/1/0 | 4/9/0 | 49/58/0 | 仅 API 桩，真实行为 RED |
| green-1 | 1/0/0 | 13/0/0 | 111/0/0 | 初轮实现 |
| final-focused | 1/0/0 | 30/0/0 | 216/0/5 | 最后源码快照 |

final 新 handoff 子集：15 top、116 named PASS / 0 FAIL / 5 UID SKIP；原相关 lease/state/publication 回归 100 named PASS。named 含 top/subtest，不是叶子数。五个 SKIP 精确为 `TestOwnerRecoveryHandoffRejectsForeignUID/{state,spool,launch,lock,ledger}`：本机 `chown` foreign UID 返回 EPERM，ROOT 后续补验权限矩阵。

真实 filesystem/lease 验证包括六种 budget 状态（initial/spent/reserved/full5/reserved5/unavailable）、普通旧 scope open/commit 拒绝、prepared prior→next、prepared next ACK loss replay、applied next inode/mtime 完全只读、applied prior rollback、正反向 full-record hashes、七 OwnerPending 阶段/starting、缺文件零创建、flock 持锁与 concurrent opens、scope/proof/path/authority/permission/link/inode/bytes/mtime 反例。readonly 和拒绝路径用实际树 snapshot 检查。

Publication 故障分三类现场：before-create 无 stage；rename 前保留原 ledger + 本次 stage；rename 后保留 next ledger、不恢复 prior。覆盖七私有 hook 注入 EIO 边界，以及关闭真实 stage FD 导致实际 write/sync 失败。stage_synced/before_rename/renamed 三时段分别测试 inode/bytes/hardlink/mtime 漂移；Close 后现场不变，prepared 重开不 GC。没有实际断电、文件系统注入内核 EIO 或跨 DB 原子性证明。

工具 `go1.26.8 darwin/arm64`，模块 `tools/orchestrator`，`TMPDIR=/private/tmp`。最终退出码 0：

```sh
TMPDIR=/private/tmp /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go test -json -count=1 -run '^Test(OwnerRecovery(Handoff|Lease)|OriginalOwnerRecovery(RejectsCorruptOrChangedState|TicketReadsRejectLedgerDrift|ExactReservationReplayDoesNotRewrite|RejectsReplacedDirectoryOrLock|UnsafeDirectoryDoesNotCreateLock|PublicationRejectsStageDrift|ReadRejectsDetachedLedger))' ./internal/host
TMPDIR=/private/tmp /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go vet ./internal/host
```

其余各阶段 selector 为 `^TestOwnerRecoveryHandoff`。JSON/stderr 在 `../data/`；`counts.json` 从实际 Go action 计数。`vet.log` 空输出 exit0，gofmt/diff-check 无输出。两个新 Go 文件 hash 见 `source-sha256.json`，源码冻结后仅整理文档/证据。

## 未验收/边界

独立 FS 审查已 PASS；race、Linux/container、foreign UID/native/full 均由 ROOT 持有，本子任务未执行或声称通过。五个 UID SKIP 是未覆盖范围，不算已验收。不连接 DB/RPC/runtime，不处理 bound 未 Applied 死亡放弃或 intended crash；文件层不等于 B3 完成。后续 GC 需独立归属/权威规则，不由本层删除不确定文件。

保留本任务全部 RED/最终证据与必要 metadata；API 暂存文件已移回源码，未遗留临时备份。没有删除归属不明文件，未 merge/install/push。ROOT 先决补丁与本任务提交分开，交还后不再写本 WT。
