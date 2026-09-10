# 方案证据与未验证事项

核验日期：2026-09-10。本文只记录帮助、协议和一手文档证据，不代表运行中的原生 CLI 集成已验证。

## 本地 Codex 协议

已通过本地 `codex-cli 0.153.4` 的帮助以及下列命令生成的 schema 核验：

```text
codex queue --help
codex app-server --help
codex app-server generate-json-schema --experimental --out <task-temp-dir>/schema
```

关键结果：

- `codex queue` 存在 `--thread` 和 `--message` 参数；帮助本身不能证明队列会自动唤醒原生会话。
- `TurnStartParams.toolOutput` 存在，引用 `TurnToolOutput`，要求 `name` 和 `output`，可带 `namespace`。
- `Thread.id` 是具体 thread 的标识；`Thread.sessionId` 被定义为同一 session tree 内共享的标识，不能用后者独自路由任务回调。
- `Thread.canAcceptDirectInput` 描述 loaded thread 是否能接受直接输入，不代表用户的终端仍然打开。
- 已检查的公开请求/通知枚举没有发现明确的本地 CLI 界面 attach/detach 或订阅者计数接口。这个观察不是“产品绝对没有该能力”的证明，必须进一步做端到端验证。

生成的完整 schema 仅为此次核验的临时材料，不作为交付物保留。

## 官方资料

1. [Codex App Server：启动轮次](https://learn.chatgpt.com/docs/app-server#start-a-turn)：支持 `turn/start` 携带 `toolOutput`；已有普通轮次时外部工具输出会排队。需要连接真正持有目标会话的服务。
2. [Codex Hooks](https://learn.chatgpt.com/docs/hooks)：命令 Hook 可辅助身份注册；异步 Hook 不自动开启空闲轮次。安装/定义改变后还需正常信任流程。Hook 身份、结束事件与实际 TUI 附着的关系需实测，不能只用 SessionEnd 推断关闭。
3. [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)：提供一组进程的管理能力。能否按设计脱离启动者生命周期、处理嵌套 Job 与终端关闭，需要原生 Windows 用例。
4. [Windows CreateProcessW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessw)：进程创建和命令行边界的一手参考。不能把 Unix shell 的参数或信号行为直接移植到 Windows。
5. [Git merge](https://git-scm.com/docs/git-merge)：默认可覆盖 ignored 文件，autostash 也可由配置开启；设计显式使用 no-overwrite-ignore/no-autostash。
6. [Git update-ref](https://git-scm.com/docs/git-update-ref)：带旧 OID 的引用更新/删除可做条件检查，不代表工作树文件更新事务。
7. [SQLite synchronous](https://sqlite.org/pragma.html#pragma_synchronous)：持久化设置影响断电保证；协议必须区分用户态写入、事务提交、文件同步和硬件损坏。
8. [Go 路径操作](https://go.dev/blog/osroot)、[Go os API](https://pkg.go.dev/os)：目录句柄相对操作用于限制路径替换竞态；依赖具体 API 的工具链版本仍需锁定和两平台验证。

## 本轮无模型 Git 语义实验

由 `review_git_cleanup` 子 Agent 在独立合成仓库执行，未使用用户仓库。环境：macOS 26.3.1 arm64，`git version 2.39.3 (Apple Git-146)`。这是 Git 行为证据，不是本工具实现测试。临时实验根已由执行者删除并确认不存在。

| 实验 | 可重现的输入结构与关键命令 | 实际观察 |
|---|---|---|
| E1 | main为old，另有改变f的candidate；`git update-ref refs/heads/main candidate base` | HEAD变candidate，index/worktree仍old，status显示`M  f` |
| E2 | 主树有ignored用户文件，candidate追踪同路径；比较merge是否加`--no-overwrite-ignore` | 默认退出0并覆盖；加保护参数退出1，原HEAD和内容不变 |
| E3 | 新worktree仅多出ignored.tmp；普通status后不带force的worktree remove | status为空，remove退出0并删除该目录及ignored内容 |
| E4 | merge.autoStash=true，主树有冲突脏修改；比较是否加`--no-autostash` | 普通ff-only自动stash且留下冲突；显式禁止后拒绝，原内容/stash列表保持 |
| E5 | f设assume-unchanged或skip-worktree且本地改变，candidate需更新f | status为空，但受限merge仍拒绝覆盖并保留本地内容 |

上述结果仅证明这些具体 Git 语义；未执行本工具的 IntegrationAttempt、候选审查、故障对账和 Windows 清理实现。

## 仍未证明的产品能力

- 不使用 wrapper 的原生 CLI 准确会话绑定、空闲唤醒、关闭/恢复后补交。
- 同一 thread 被用户和桥接器同时输入时的顺序和去重。
- RPC 已接收但响应丢失时，是否存在足够的信息进行可靠对账。
- 各 CLI 在原有账号、目录信任和权限设置下的 headless 写入、测试、提问及续接。
- 原生 Windows 上各 CLI 的可用组合、shim、后台进程树和取消行为。
- CPU、内存、磁盘开销，不能以语言选择替代测量。
- 两平台的最终候选审查绑定、当前分支整合事务/并发窗口、文件登记与清理崩溃恢复；少量Git实验不能替代G3。
- Hook正常信任后的真实接入、来源Host环境/配置连续性、spool/启动UNKNOWN/磁盘满等恢复协议。

对应验收用例见 [acceptance-v0.1.md](acceptance-v0.1.md)。

本轮文档评审的修复与覆盖记录见 [review-report.md](review-report.md)；设计评审通过与运行验收通过分别记录。
