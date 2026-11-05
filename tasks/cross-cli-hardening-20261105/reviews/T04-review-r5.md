# T04 r5 独立复核：PASS

结论绑定冻结 T04 r5 工作树；本轮未发现必须修复的代码正确性问题。r3 的未知 hardlink 计费释放问题和 r4 的源路径替换误删问题，均由当前协议及永久回归覆盖。所有本轮测试会话已结束，源码前后逐文件核验均为零差异。本结论不代表 integration 的 T07 server/actionCtx 合并验收，也不代表全项目完成。

## 版本与范围

- 工作树：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t04`，baseline `2347d34ce1e5ca4e40751f53064f3d99001f4832`。
- source-manifest-r5.json SHA256：`b9c15ca0dd61c59214aa2612b2b311cc51af56e7bf1fc810494cb22a4607112f`；2778 个列明文件，hash-before.json / hash-after.json 均零差异。
- T04-complete-r5.patch SHA256：`2e4e061611d5fe6a2ecb856d194ade91071ae6761ed956aad8a386a2849791f1`。
- 只读 T07 依赖 owner_action_guard.go SHA256：`d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150`。完整 patch 不包含该文件，也不包含 legacy QueueRequestChanges 的既有 snapshot hook；该依赖需先整合。
- 已读项目 AGENTS.md、运行/清理协议、ADR-004、implementation-report、delivery/validation-r5、完整 patch 结构及相关实现、r3/r4 审查报告。r4→r5 清单差异仅为 rework_objects.go、rework_quarantine.go、两份平台 capture helper、rework_refs_test.go。

## GC 修复与恢复取舍

- `internal/store/rework_objects.go:830` 对全部已知 `.stage/.json` 名称进行 preflight，核对 regular/uid/0600/device/inode，并要求 nlink 精确等于已知名称数量；持有 no-follow FD。额外 hardlink 在任何删除前拒绝。
- `internal/store/rework_objects.go:698` 与 `internal/store/rework_quarantine.go:23` 只枚举当前 upload 的隔离屏障名称，不进入、接管或删除既有隔离目录。`rework_quarantine.go:41` 创建随机 0700 隔离目录并持有目录能力与 FD；正常同一 upload 的两个对象共享该目录。
- `internal/store/rework_capture_darwin.go:12` 用 Darwin `RenameatxNp(RENAME_EXCL)` 原子捕获源目录项，目标不覆盖。`rework_quarantine.go:90` 在 source/quarantine fsync 后检查实际捕获对象的完整登记身份、类型、权限及剩余 nlink，通过 no-follow FD 再核捕获身份后才在私有 namespace unlink。源路径替换成未知 file/symlink/directory 时，该捕获条目保留，不按旧检查结果删除。
- `internal/store/rework_objects.go:752` 在目录 sync 和原始 held-FD nlink==0 之后，才 DELETE registry 并由 trigger 释放对象计费。失败保留 deleting row/charge。所有 FD 在当前 RPC 返回前关闭，锁内无跨 RPC writer。
- 保守恢复是明确且合理的合同取舍：登记 inode 无已知名称、已捕获后中断、未知/非空隔离目录都不能证明对象彻底消失，故保留计费并要求明确 owner 清理决定，重放不猜测成功、不自动接管。非 Darwin 在创建隔离目录前返回明确 unsupported。
- `internal/store/rework_objects.go:774` 的 `removed` 仅表示正文回收；`cleanup_reason=upload_tombstone_and_quarantine_barrier_retained` 明示仍保留 tombstone 与空屏障。4096 是 upload 的逻辑元数据配额，不能解释为 APFS 实际块计量。本次不声称完整 runtime 清理或自动 crash 回收完成。

## 其他合同核验

- `internal/store/rework_objects.go:255` 拒绝旧 v1/v2 或高 revision 无 context 的自动升级；rev1 prompt 直接封存，后续复用 immutable brief；`:562` 将历史 condition 按旧到新追加并逐个校验。Host `internal/host/rework_refs.go:14` 再核 tuple、marker、独立 trusted state 与历史顺序；读取同一有界 bytes 作 hash/parse/assemble。
- `internal/adapter/rework_refs.go:18` 保持 256KiB body、8KiB manifest、16KiB chunk、至多 brief+2 conditions、512KiB assembly；Store `rework_objects.go:592` 与 Host `cmd/orchestrator/main.go:1136` 再检查实际序列化 stdin。没有改变旧 16KiB adapter/64KiB IPC 限制。
- reservation 在文件创建前持久化且包含 size+4096/object 与 4096/upload；同锁串行 chunk/abort/GC；commit 将 pins、decision、revision/ready sequence、能力撤销放在同一 transaction。commit replay 先于动态 availability，仍核 current owner/snapshot。
- HostHello/Ready 能力独立，queue/claim/pending/summary 保守检查。旧 revision handled ACK 的 `internal/store/delivery.go:201` 仅把允许的 decision kind 从 v2 扩到 v2/v3，精确 source tuple 条件保持。
- v1/v2 bytes/digest golden 在本次独立合同回归中通过。r3/r4 已审的未改业务部分与历史证据按其原范围复用，未冒充 r5 全树复测。

## 独立实际验证

环境：Go1.26.10 darwin/arm64；TMPDIR=/private/tmp，GOENV=off，GOTOOLCHAIN=local，GOPROXY=off；固定 Codex0.166.0 PATH；独立 review-final/gocache。准确 argv、环境、退出码见 environment.json，报告 SHA 和计数见 counts.json。

| 报告 | 命令范围 | pass events | 顶层 PASS | fail / skip | 退出码 |
|---|---|---:|---:|---:|---:|
| normal.json | store `^TestReworkRefs` | 51 | 23 | 0 / 0 | 0 |
| race.json | 同范围 `-race` | 51 | 23 | 0 / 0 | 0 |
| contract-normal.json | adapter/host/CLI/store golden、legacy owner、Host refs、CLI V3、ACK/Delivery | 133 | 44 | 0 / 0 | 0 |

所有 run 测试都有终态、全部 package PASS；race 无 DATA RACE。永久用例实际覆盖未知 hardlink、late hardlink、三种目录项替换身份保存、SQLite close/reopen 后三次 GC 重放、正常两对象共用一个屏障、tombstone 计费。没有另写探测测试或覆盖旧 reviewer 脚本。

`git diff --check` 退出0。另独立核对 validation-r5 所列全部报告的 SHA，均相符（prior-evidence-check.json）；其中 r5 实施 normal/race 各51/23与本轮计数相同，r3 全量 race 的1307 PASS/8 skip、593顶层PASS/8 skip仅作未受影响的历史证据。本轮未重跑全树 normal/race、vet/build；实施者 r5 vet/build 退出0记录与空日志 hash匹配，不冒充本轮独立执行。

## 限制与交接

没有运行模型、真实 provider、付费调用、安装包 v3 端到端或 native live-parent/typed-resume。未验收非 Darwin runtime。T07 server authorizeOwnerAction/actionCtx 合并仍需 root 保留其六动作 guard/local-submit gate，并对 v3 五 phase 作组合回归及独立复核；当前 base PASS 不覆盖这些 integration 差异。

只写外部 review-final 证据，未改产品、未派生、未提交/push。所有本轮会话已结束；无待清理的产品临时文件。原始报告、hash、环境记录及独立 Go cache 保留用于复核，均有明确用途。
