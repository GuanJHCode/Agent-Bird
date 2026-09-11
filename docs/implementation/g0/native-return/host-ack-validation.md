# 单事件 owner / revision / ACK 探针验证

日期：2026-09-11。分支 `exp/g0-synthetic-host`；基于 `0d9de85` 增量实现。状态：本地必要验证完成，等待独立审查和真实原生 CLI 场景验证；本报告不放行或宣告 G0 通过。

代码与合同位于 [`tools/g0-return-lab/ACK.md`](../../../../tools/g0-return-lab/ACK.md)、`main.go`、`ack.go`、`ack_test.go`。仍为 macOS 单事件合成 Host，没有 DB、第三方依赖、真实 CLI adapter、调度器或分布式 lease。

## 已实现合同

- `start` 可选 `--controller-thread`，显式传入时必须非空、安全且等于进程已有 `CODEX_THREAD_ID`。owner 控制文件先建立，worker 再发布带 `controller_thread` 和 `task_revision:1` 的不可变 job 元数据；既有 ready 握手继续核验实际子 PID 与磁盘归属信息。局部启动失败或 post-spawn 不确定不报告成功。
- 无 owner 的旧 `start` 仍输出七字段，全部 `read` / `wait` 仍输出四字段，旧基线二进制没有替换。其他 thread 可进行只读观察；直接声明另一控制 thread 的 ID 不会通过进程环境与存储 owner 的双重比较。
- `inspect` 显示当前 control `revision`、不可变 producer `task_revision`、取消状态、真实结果和稳定 `event_hash`。hash 绑定 owner、nonce、产生 revision 与四字段结果，排除当前 revision、decision 和 command ID。
- `ack` 必须带 owner、revision、hash、command ID 和固定 decision。action slot 不含 decision 或 command ID。本探针只有一个 task revision 为 `1` 的结果事件，对应一个 `ack-r1.json`。同 decision 重复 ACK，甚至更换 command UUID，也返回同一原始记录与首次 command ID；冲突 decision/hash、其他 thread、旧 revision、取消状态均拒绝。
- ACK、`decision_count` 与 `effect_count` 在同一个原子记录中提交。每条记录 `decision_count=1`；只有 `handled` 为 `effect_count=1`，`waiting_user` / `stale` / `rejected` 均为 `0`。没有独立效果计数文件或跨文件事务声明。
- `revise` 使用 expected-current revision 比较并单调加一，可用 `--cancel` 进入终态。不会重启或杀死 worker，不改变产生 revision，不允许把旧事件改传新 revision 作为新工作完成。取消需要合法控制状态与任务归属，不依赖结果文件能否解码。
- `inspect` / `ack` / `revise` 共用同一个不替换的 OS `flock` inode，等待上限五秒、检查间隔 50ms。只有 `control.json` 可在持锁期间由已同步 stage 原子替换。ACK、job、结果均不覆盖。ACK 和 revise 的中断 stage 在持锁后经文件类型/所有者/权限/大小核对再清理；已发布目标保持。

所有检查仅防止实验中意外的跨 thread / fork 使用，不抵御同用户伪造环境或文件。所有字段和文件读取沿用私有目录及有界 schema；错误输出只含固定错误码。

## TDD 与真实故障证据

先写真实 CLI / 本地文件 / 子进程测试，在旧实现上执行：

`go test -count=1 -timeout=60s -run 'Test(Owned|Inspect|ACK|Revision|Concurrent)' -v ./...`

退出 `1`（`FAIL g0-return-lab 2.268s`）：匹配 owner、ACK、revision、inspect 尚未实现，所有新行为用例因 `invalid_args` 或缺少 `thread_mismatch` 失败。实现后相同新用例返回 `0`（`ok g0-return-lab 4.881s`）。

随后边界测试复现 `revise` 误依赖结果 JSON：损坏已完成结果后请求取消，返回 `invalid_artifact`，定向测试退出 `1`（`2.132s`）。根因是共用了完成结果校验路径；将 revise 限制为控制/任务元数据校验后，纳入下述最后全套测试通过。

响应丢失测试没有生产 test 开关：测试先填满实际 stdout pipe，再启动真实 ACK 子进程；观察到 `ack-r1.json` 发布后杀死仍被 stdout 阻塞的进程。后续换 command ID 重试恢复首次 ACK，仍为单条 `decision_count=1`、`effect_count=1` 的记录。

## 最后一次源码修改后的验证

使用已有 `tasks/g0-macos/data/go/bin/go`，版本 `go1.26.8 darwin/arm64`。工作目录为 `tools/g0-return-lab/`。`GOCACHE` 为本模块 `tmp/cache/`，`GOTMPDIR` 和测试 `TMPDIR` 为 `tmp/build/`。没有联网安装或更改个人配置。

| 命令 | 退出码 | 证据 |
| --- | --- | --- |
| `go test -count=1 -timeout=90s -v ./...` | `0` | `PASS`；`ok g0-return-lab 15.168s`；包含原基线全部用例 |
| `G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=120s -v ./...` | `0` | `PASS`；`ok g0-return-lab 16.765s`；CLI 子进程也以 `-race` 构建 |
| `go vet ./...` | `0` | 无诊断 |
| `go build -o data/g0-return-lab-ack .` | `0` | `Mach-O 64-bit executable arm64` |

主要验收证据：

| 要求 | 直接证据 |
| --- | --- |
| 错误 thread + 同 nonce 拒绝 | `TestOwnedStartRequiresMatchingInjectedThread`、`TestACKRejectsOtherThreadHashAndInvalidArguments`；纯合成子进程环境 |
| 同 / 冲突 ACK、更换 UUID | `TestACKDuplicateCommandsUseOneDecisionAndEffectRecord`；四种 decision，原 inode 与修改时间不变，冲突决策失败 |
| 产生 revision 不随当前 revision 变化 | `TestInspectBindsActualResultAndProducerRevision`、`TestRevisionBeforeCompletionAndCancellationRejectOldWork`；旧 revision 拒绝，新 revision 也不能重标旧事件 |
| 取消后 ACK 拒绝、worker 继续 | `TestCancelDoesNotKillPendingSyntheticWorker`；取消后真实 worker 仍落盘，ACK 返回 `cancelled` |
| 输出丢失 / 进程死亡后恢复 | `TestACKRecoversAfterCommittedProcessDiesBeforeResponse`；已提交但 stdout 阻塞的真实子进程被杀后恢复原记录 |
| 至少 20 并发 ACK 仍一效果 | `TestConcurrentACKsProduceOneEffect`；20 个真实 CLI 进程、不同 command ID 返回相同记录，仅一个 ACK 文件 |
| ACK 与 revise 串行、旧重试失效 | `TestACKAndReviseSerializeAndLateRetryFails`；并发操作完成后 current revision 为 2，晚到 ACK 失败 |
| 同一 OS 锁与有界等待 | `TestControlCommandsShareBoundedOSLock`；外部实际持锁时三个命令均约五秒返回 `lock_timeout`，没有 ACK 写入；正常测试 5.16s、race 5.19s |
| 中断 stage 清理与不可信文件拒绝 | `TestACKRecoversUnpublishedStageAndRejectsUnsafeLock`、`TestReviseRecoversStageWithoutDependingOnResultContents`；局部 stage 可恢复，公开权限 lock 拒绝 |

新交付二进制：`tools/g0-return-lab/data/g0-return-lab-ack`；SHA-256：`8ed9567aaf95867204f448aa5bd498a7ce5895790a0f24149419d9b460c962db`。

旧基线二进制 `data/g0-return-lab` 的 SHA-256 始终为 `cf7bfc0acf6f3348210c50c75f9a0ea85b6a56e1abafbbcec85156580dc40cec`。新构建没有替换正在运行的旧文件。

## 声明边界

`effect_count` 是该单事件合成 action slot 的持久计数。它不证明真实第三方 CLI 派发、模型答案续接或外部副作用恰好一次。没有多事件部分 ACK、生产恢复器或可信同用户认证。

上述环境全部是明确标记的合成子进程环境，不是伪造的原生通过证据，没有操作个人 Codex DB 或权限。真实 native thread 注入、主控/子 Agent 返回、用户竞争、C04/C09/C11 端到端行为仍由原生 runner 和独立审查判断。

原生 `update_goal` 自身没有与本地文件锁配合的 revision CAS；Go 层拒绝旧 ACK，不能原子撤销模型已经提交的 Goal complete。此边界独立保留，本探针不因本地计数通过而将其视为解决。

OS `flock` 等待和解析长度有界，但正常文件 I/O 仍服从操作系统行为；不宣称磁盘停滞时具有硬实时上限，也没有掉电持久性验证。

## 临时资源

测试 harness 自动删除测试目录和子进程二进制。收尾核对无本任务临时执行进程后，列出并删除 `tmp/build/`、`tmp/cache/`。保留新旧两个明确交付二进制、源代码和验证记录，供独立审查与原生运行使用。
