# 实施共享契约

## 基底与写入归属

- 原始树与隔离快照见 `../data/implementation-workspace.json`，不得覆盖用户原有变更。
- M1 唯一负责人 `implement_product_entry`：`source` worktree 的 CLI/owner/controller/dispatch/reconcile/routing 及必要 process helper。
- Native 编码唯一负责人 `implement_native_coding`：`native-source` worktree 的 adapter profile/native admission、Host/native/gateway、install/helper identity 与 Python auth/helper。不改 CLI、store/coordinator、`adapter/workspace.go` 或 `host/candidate.go`。
- M2 在 M1 稳定和审查后实施：store/coordinator/IPC、CLI request-changes、`adapter/workspace.go` 和 `host/candidate.go` 的 v2 context。需由主会话切换唯一负责人。
- 各 worktree、Go cache、测试输出与运行时目录隔离；真实模型试验串行，不与子 agent 实施并行。

## 高层入口

保留已有入口，新增/复用 `controller start`、`routing status`、`task dispatch`、`task wait`、`task reconcile`。wrapper、实际二进制、Skill 和文档必须一致。

新增 `task request-changes --handle H --request private.json`（多任务显式指定 task），请求必须明确原任务 revision、candidate OID、feedback、acceptance 和稳定 command ID。工具层可以从原 handle 的 summary/collect 补充源事件绑定，但不得自动把旧请求改为最新 revision。

底层使用独立 `RequestChanges` IPC 和 owner spec，必须携带并持久化 task/revision、源 result event ID/revision/hash/action slot/segment、candidate OID、feedback、acceptance、command ID。Coordinator/Store 不得按 task 自动替换 latest event。

## 主审直接返工

- 保留旧 `QueueRework` 四阶段与 `ReworkContext v1` 字节/digest 兼容性。
- 新路径只适用于独立、受管的 typed implementer、`owner_review` 单任务、独占预算组、无前后依赖/session/fallback/candidate action。
- 仅在 `result_ready`、latest segment 已确认 exited/result、无 active/unknown/open question、Host exactly ready、provider 仍可用且预算/存储允许时接受。
- 重新验证 artifact size/SHA、Host candidate binding（run/task/attempt/segment/work revision/plan revision）、repo/base/tree/candidate OID 与候选收据。
- 与 accept/reject 在同一事务域校验 source event/action slot/command ID 互斥；同 command 与同规范请求仅重放原 receipt，冲突请求拒绝。
- 复用 `retry_decisions.revision_payload` 保存不可变 owner 决定，不伪造独立 reviewer 事件。
- `ReworkContext v2` 明确 `source=owner`、owner decision ID 与规范化完整 spec 摘要；由 Store 构造，外部提交不得自行注入。
- 下一 revision 保留 provider lock/profile/repo/paths，以上一 candidate 为 BaseOID，使用新的工作区；事务性推进 revision/ready sequence 并撤销旧 report capability。
- 技术 retry 不增加 work revision，审查返工增加 work revision；分别展示计数，但保留同 budget group 的总 attempt/active 上限，不默认增加授权额度。max_attempts=1 时不可返工，必须明确投影原因。
- summary 的 allowed_actions 与写路径复用资格检查；新 v2 协议需要 coordinator/Host capability 门禁，不静默交给旧运行时。
- 已裁定最小 Host 能力表示为 `HostHello.owner_request_changes_v2` 布尔字段，`runtime_hosts` 同名 INTEGER 默认 0，schema 8→9 事务迁移；认证 hello/reconnect 覆写当前声明，旧 Host 仍可运行旧任务，但排队和启动 v2 都必须验证能力。

## 原生受管编码

- Codex 必须经过材料化候选、role-aware native named policy、冻结候选；保留精确 main/companion pin、auth projection、唯一 refresh tripwire 和 ExactEnv。
- 写权限由 Host 构造，只允许授权候选文件和必需 scratch/report；拒绝 Git 元数据、state/control、认证/config 写入及 network/web/MCP/递归代理。
- Claude gateway 的编码也必须经过网关隔离路径；保持私有 HOME、单 API credential、strict empty MCP/settings、source home/keychain 隔离。写模式使用实际支持的正向 file-tool 列表，Bash/delegation/web/MCP 继续拒绝；测试由 Host validation 执行。
- Claude 原子 Write 的原生试验表明，需要同目录 `<exact-authorized-file>.tmp.<digits>.<12hex>` 临时写入。仅允许每个授权文件派生的严格 anchored/正确转义规则，不开放父目录或普通邻接文件；启动前拒绝匹配的现存文件、链接或目录碰撞，冻结时拒绝所有残留/范围外变更。模板必须按实际受测版本验证，不从只读 catalog 推断 coding 支持。
- 未建立的版本/能力继续拒绝，不用提示词限制代替系统权限，也不改变账户或模型来制造成功。

## 必要验收

1. Go full/race/vet/build、Python 相关套件与实际打包入口；记录新报告的 passed/failed/errors/skipped 及源码绑定。
2. 新 owner scope 和递归派发拒绝、dispatch 幂等/提交恢复/旧 coordinator 门禁。
3. 新主审返工正常路径、同 command 重放、冲突请求、旧 revision accept/rework 拒绝、accept/request_changes 并发互斥、ACK 前后结果一致。
4. Host/进程未知、未决问题、provider 关闭、attempt/active/storage 耗尽与恢复；旧 v1 四阶段全量回归。
5. 真实 Codex 主会话委派 Claude 编码、读取实际 diff/测试证据、打回一次、基于前一 candidate 完成下一 revision、再次复核后 accept；反向 Claude→Codex 单独验收。
6. bounded wait/collect 只能证明同一工具调用主会话的可靠回收；任意 TUI 主动唤醒或 MCP Tasks 必须另有宿主证据。

当前实测：Codex login status 成功；Claude 2.1.316 first-party未登录，用户已提供既有网关及明确模型，仅用于本次任务内存级子进程环境，不改账户或全局配置。不得用离线fixture、历史成功或本地stub代替真实模型验收。
