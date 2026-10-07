# Worker report 附件接管验收

本批完成 M3 的 report 链路：schema20、私有有界复制、事务引用与公开 CLI
重放。M3、F02 和 F01–F13 总目标仍未完成。源码基线为 `8c949d7`；全部实现和
验收位于 capability-closure 的 integration worktree，不替换用户安装。

## 已实现行为

- question/result/failure 附件最多1MiB。第一次事务验证 token、原 producer、
  run/task/attempt/segment/revision/epoch、sequence 和 canonical payload，持久化
  不可变 intake 与额度预占。progress 保持原有通道。
- 锁外复制至固定 `managed-artifacts/<intake-id>/data.bin`。私有目录0700、
  文件0600；拒绝 symlink、hardlink、特殊文件、越界源及不安全布局。采用固定
  stage、32KiB buffer、摘要/长度与 FD/父目录身份检查、fsync 和独占发布。
- 第二次事务前重新打开并复验原目录、intent、锁和数据对象，持对象锁完成重新
  授权、event/ref、retained bytes 与 phase 提交。外部接口不接受调用者提供的 proof。
- 已提交精确重放不再读取源文件；未完成 intake 保留原 reservation，不重复计费。
  完整 stage 可以在源消失后恢复。第二次事务失败、数据库重开、并发同请求均有测试。
- raw bytes 与 control metadata 分账。报告可使用 critical disk reserve，但不能
  侵占 min-free、SQLite overhead、已有未物化 promise 或 artifact allowance。
  旧记录不授予文件权限，仍保留旧计费，未声称全写入点已受2GiB约束。
- 修复原 owner 的报告端点：由已验证 StateDir 派生 `coordinator.sock`；能力登记和
  owner 生命周期仍使用 signed `owner.sock` RPC。未开放 owner socket 的无签名接口。

## 有效验证

Go1.26.8 darwin/arm64；`TMPDIR=/private/tmp`。Go JSON 日志同时统计顶层用例与
包含子用例的 named 数，以下均0失败、0跳过。原日志、二进制、SHA 和终态证据在
本任务 `data/`，汇总为 `data/root-final-receipt.json`。

| 验证 | 顶层 / named | 证据 |
|---|---:|---|
| 全量 Store，非缓存 | 393 / 1120 | `store-full-v2.jsonl`，session51801，71.790s |
| artifacts 全包 race | 8 / 33 | `artifacts-final-race-v1.jsonl`，43945 |
| report、额度与相关续接 race | 26 / 87 | `store-final-race-v1.jsonl`，51213，116.314s |
| Host report/owner execution race | 17 / 48 | `host-public-race-v1.jsonl`，29153 |
| 原活 worker 跨 Coordinator 重连 race | 1 / 1 | 同上，C02 原 capability、PID/birth、单次执行账本 |
| 原 owner + worker 公开 report CLI race | 1 / 1 | 同上，删源、两次 ACK 相同、唯一 event、managed 内容 |
| AGY/Claude 受控原生报告权限负例 race | 1 / 3 | `native-authority-race-v1.jsonl`，96230 |
| Docker Linux arm64 artifacts | 8 / 33 | `linux/core-v1-receipt.json`，64027 |
| Docker Linux arm64 report/相关额度 | 28 / 95 | `linux/store-v1-receipt.json`，62722 |
| 全模块 build / vet | exit0 / exit0 | `build-final.log` / `vet-final.log` |

全量 Store 后仅加强两条测试的 Close/Open 断言；最终 affected race 和 Linux 二进制
已覆盖这两条最终测试。Store 生产源码未再改变。artifacts 最终源码未改变，因此复用
其全包 race。Host 只改报告端点，相关 owner/report 与实际公开链路另行验证。

CLI 测试中的 orchestrator 子进程是普通 `go build`，race 覆盖测试进程和相应
插桩 helper，不把普通子 binary 声称为 race 插桩。AGY/Claude 使用受控 Go actor，
未调用真实 Provider 模型或账号；日志包含原执行 group absent 和业务失败/无报告权限
证据。Linux 使用已缓存镜像、无网络、只读根、nobody、无 capability、512MiB/2CPU
限制，不覆盖 Linux race、公开 CLI、Provider sandbox 或安装链。

## 失败与修正记录

- 46807：公开 Store 实际返回 provider 源路径，证明删源后无法保证已 ACK 产物。
  14944 仅为 fixture 目录权限错误，不计业务 RED。
- 48106：7个 copy 后替换/缺失/伪 proof 负例仍被 ACK；增加提交前物理复验与持锁
  事务。84524 中这些负例已通过，但该组合命令另有 critical reserve 回归，不能记全绿。
- critical reserve 回归由原 `TestReportReplayBindsKindAndProgressCannotConsumeCriticalReserve`
  捕获；报告专用准入只移除 protected critical reserve，其余 promise/额度检查保留。
  另补恰好足够/少1字节和已有 prepared promise 的边界。
- 23152：私有 Go overlay 禁用完整 stage 恢复后的负对照，after-sync/before-rename
  删源子例失败。它是实施后的负对照，不冒充先于实现的 TDD RED。
- 63404：全量 Store 仅3个顶层、7个 named 续接计数断言失败，旧独立计数漏掉新的
  reservation/intake/ref metadata。补全逐字段计数，保留原额度/回滚/重放断言；51801全绿。
- 54026：旧 CLI fixture 的 `/tmp` alias 在 owner canonical 校验处失败，先修测试目录。
  6719/47159 随后揭示真实接线错误：worker 已 Start/Exited2，报告能力文件指向
  `owner.sock`，第一次 report 收 EOF，Store 无 intake/event。修正端点后15351普通和
  29153 race均通过。47159现场已逐文件核验归档，未删失败日志。
- 其他早期失败包括 rename 改变 ctime、joined error 的测试比较、旧 body+raw 计费断言；
  保留原日志，分别按身份/mtime检查、`errors.Is` 和分账契约修正。没有删除失败断言或
  放宽权限来制造通过。

独立只读审查覆盖 intake 引擎、Store/schema、原对象复验、critical reserve 增量、
计数/阈值/CLI/reopen fixtures 和最后 owner 端点修正，未留 blocker；测试结果由 ROOT
读取实际日志验收，不以审查者自述代替运行证据。

## 资源与剩余范围

两个 Linux CID 均正常终态、非 OOM，精确删除并 inspect 确认不存在：

- core：`83e0899f919d780b1f8c1143544d0f122c46e73fe45e75dd89ae3e08bb5b6a5c`
- Store：`6ddc6248b97bd402b81b178b9d59dfd6fc000268b73978fa118b1bb1b2d95246`

成功 native fixture 清理其私有目录。47159失败现场归档为0600的
`data/public-cli-v3-failure.tar.gz`，逐文件摘要核对后移除原临时目录；临时负对照 overlay
已删除，负对照源码与日志保留。详见 `data/cleanup-receipt.json`。无本批后台测试或容器。

仍待实现：所有 Host terminal/progress/prelaunch 写入点的派发前预占与物理限额，
legacy_pending 历史核算，diagnostics retained 验证，显式删除/永久 tombstone/崩溃恢复，
以及受管2GiB总额度、native flood/RSS 和实际磁盘故障验收。当前 checkpoint 注入与
tmpfs 容器不等于真实断电或物理磁盘故障。Intel Mac/Windows 原生因用户暂无环境延期。
F01–F13继续推进；未替换安装、推送或发布。
