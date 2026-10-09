# F10 server-owned bootstrap publication 验收

本轮闭合 direct IPC 的发布权限和文件恢复：新 CLI 使用 `rebind_owner_published`，Coordinator 持有文件 lease 并完成 publication；schema35 journal 将已提交的 owner rebind 与待发布的固定字节绑定。最终固定产品树为 `c9e1222115f3124b0a37070ea49e46dc39207b10`，Go 模块树 `0d428fa6094a594a2be2cfb423053e078bdc4f97`。ROOT 实际回执见 [final-receipt.json](../data/final-receipt.json)。集成后的包须重新验证，旧 F13 包只用于历史协议拒绝。

## 行为与边界

- 新协议先认证 run，再解析可信 launch/root，以文件 lease → Coordinator registration → Store transaction 顺序执行。旧 `rebind_owner` 在获取 lease 或修改权限前明确返回 `owner_rebind_publication_protocol_required`；新 CLI 面对缺 capability 的旧 Coordinator 返回 `coordinator_upgrade_required`，没有兼容回退。
- journal 冻结 root 物理身份、原/新 owner 与 proof/generation、prior hash、expected canonical bytes/hash。Prepare 与 owner rebind 同事务；完整 UTF-8 控制记录、固定 128-byte 可变字段 reserve 和一份 expected scratch 在写入前计费。Finalize 同事务重核 current owner/唯一 launch/root，只释放一次 scratch。失败 COMMIT 验证池内及独立连接回滚。
- fresh 拒绝同 launch 孤立 stage；pending 只接受该 journal 的 prior/expected 与准确 stage；published replay 必须实际 named 文件为 expected。稳定 stage 的原 inode、0600/same UID/nlink、长度、mtime、exact prefix 均受核验；FileSync → rename → directory Sync → readback 后才提交 published。重开不自动 finalize。
- pending gate 在实际 Store 事务内覆盖增加执行权的路径及 replay 早返；认证后的 Query/Status、只读观察及 Unbind 保持可用。测试中注入 pending 的部分仅验证 gate predicate，不构造进程退出证明。UNKNOWN、旧 v1、原 Wait/tree、预算及签名约束保持。

## 实际验证

计数是各批次自身 top / named，不相加冒充互不重复的总测试数。Go 1.26.8，macOS arm64；Linux 使用缓存 arm64 image、无网络、non-root、只读根、隔离 tmpfs。所有列为 PASS 的批次均核验原始终态、0 FAIL/SKIP；build/vet 核验 stdout/stderr 为空。

| 批次 | 固定来源与实际结果 | 用途 |
|---|---|---|
| Mac integrated race | clean `62c9e4e`，12 / 44 PASS；SHA `72647ef2fa6a09b569e0c56aaca3f44ce07782e759fc1166a2dcb40ae88c8765` | journal/quota/COMMIT、迁移、retention 交互、IPC/public 与真实历史协议拒绝；后续 gate delta 另验 |
| Mac final public race | clean `b9a1d8`，2 / 2 PASS；SHA `71ad7323507319f18dd309ff5bec257962737e5574ee5e4a116c135217b95eb4` | 最终生产 direct IPC 与实际 CLI/Coordinator/Owner/FirstHost 的 pending authorization 恢复 |
| Mac final gate race | `e90e449`；有效组合 2 / 15 PASS | 复用失败整批内 queued 独立子树 1 / 5，加修正 ACP fixture 后 Bridge 1 / 10，详见下方失败说明 |
| Mac stage race | clean `5684514`，6 / 31 PASS；SHA `4cc782bfc231bdffba218ca8815e48d9e4150db1a2f24ff6dcac03ba803a9346` | 稳定 stage、exact prefix、所有权拒绝、真实 SIGKILL 与原 Wait/tree |
| Linux stage | 6 / 31 PASS；SHA `dc8c5ac34b38ba2894501584629c97b7b933a598e031da677cfeadb01b40554e` | 与 `5684514` 相同 leaf tree；8 个真实 SIGKILL 的原 Wait/tree |
| Linux Store | clean `7aee652`，6 / 25 PASS；SHA `154b9656dc38bc8d01708c0f06c08c01952c59d9db2f0eb6fe9bb9c62f5e3ac2` | journal authority/quota/COMMIT、reconcile 与最终 queued/Bridge 门禁闭集 |
| Linux IPC | clean `f08d352`，2 / 2 PASS；SHA `887a5e4713d2305cc3e0899775f970085a7189339031340f8f52513464e78e47` | direct repair/restart/concurrency 与旧协议前置拒绝 |
| Linux public | clean `f08d352`，1 / 1 PASS；SHA `00712b9fbc3711cfa0786e9cd0d2c084ba7dfe77daa0d6e80a9a1bb4f4b5efdf` | 真实受控 native CLI/Owner/FirstHost 两代恢复，原进程 Wait |
| Build/vet | clean `f08d352` whole `go build ./...`，Store/Coordinator/hostbootstrap/IPC/CLI vet；final test-only `7aee652` Store vet，均 exit0 | f08、b9、7a 与最终 c9 的全部非 test Go 模块文件 manifest 相同，未重复 whole build |
| Windows leaf | `go build ./internal/hostbootstrap` exit0 | 仅交叉构建；不是 Windows 原生验收 |

`c9` 合并只增加测试和证据；ROOT 比较全部非 `_test.go` 的模块文件对象，manifest SHA `3c23e6ef353474b823067e97c34ba234f06ec217601e967b41242180971bdd3a`。stage 与 gate 生产/测试、Linux runner 的独立审查分别 PASS（`5684514`、`fe3bbd5` / `e90e449`、`d191fd3`）。journal/Coordinator 最终审查单独记录，不能由测试通过代替。

## 失败与证据限制

[journal / public / compatibility 原记录](../../owner-bootstrap-publication/docs/milestones.md)、[stage 原记录](../../owner-publication-stage/docs/final-report.md)与[门禁原记录](../../owner-publication-gates/docs/gate-delta-final.md)保留全部 RED、编译和 fixture FAIL。实际发现并修复 root/owner drift、prior replay、孤立 stage、pending gate 漏口、Bridge 观察误阻和 local origin context 遗漏。

`gate-delta-final-race` 整批是 exit1、13 PASS / 2 FAIL（ACP Bind fixture 缺必填路径）；只有独立终止的 queued 子树复用，Bridge 已单独修正重跑。失败时新增测试尚未跟踪且未保存源码哈希，后续仅 ACP fixture 修改的来源绑定主要依赖记录、失败位置与最终源码交叉确认，不能声称该失败批次有完整可重建源码绑定。Linux 最终检查针对 clean `7aee652` 的全部 25 named 闭集通过。前任 worker 的额外 full Store 命令缺少原终态/输出，未纳入验收，未因此重跑全 Store。

历史协议测试直接运行已留存 `3bd025e` 的原 F13 production binary（SHA `c9a09e7c1bde01c20145294ed2f702a15b91bb10138908fadb6cf951470628ba`），不是将当前 binary 改名。它证明新旧两侧在未登记 fixture run 上前置拒绝、无 mutation/死锁，不证明成功跨版本恢复或已发布版本兼容。

## 清理与未完成范围

四个精确 Linux CID 均由原 attach 终态后 finally 删除，ROOT 再次 inspect 得到 No such；仅凭原 terminal、文件 UID/nlink/inode/hash 确认后删除本轮四个 crossbinary，共 75,051,518 bytes。raw/expected/command/exit 和清理 receipt 保留。Mac stage 的 10 个已归属 fixture 根逐一 absent；公开失败 DB/bootstrap/request/log 现场保留，已结束的两份重复 binary 有单独诚实清理回执。未清未知归属内容。

本轮没有真实 Provider/model/account 调用、用户安装替换或推送/发布。没有 Intel Mac/Windows 原生、物理断电、整盘故障、真实模型/TUI/Hook 资格；F12 Formal60 仍 FAILED，旧资源样本不为该候选背书。F01–F13 整体仍未完成。
