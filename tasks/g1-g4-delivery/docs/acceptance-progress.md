# G1–G4 最终功能与验收范围

本说明以主线 `64515ff` 为基线，区分已经通过的模块与本地纵向链路、仍在根任务执行的最终验收，以及明确延期的范围。当前可以确认 G1–G3 的实现和本地产品链路已经闭环，G4 的 rc2 项目内包已经通过临时安装检查；完整原生产品用例和 G1 长时资源验收尚未结束，因此不能宣称整个产品已经验收完成。

## G1：本地任务调度与持久状态

G1 是调度中枢。它接收当前 Codex 任务提交的计划，以最多两个活动槽执行依赖图，并把任务、attempt、segment、预算、问题、结果、停止状态和待回传事件写入持久状态库。后续查询和操作使用 owner-only 的 `control_file`，不会只凭 task ID 跨任务操作。

已经通过的范围包括：双槽和依赖释放；分页 `collect`、`wait-events`、typed ACK；技术问题停止旧 segment 后在同一 attempt 携带可信 provider session 继续；显式 retry、accept、answer、stop、resume；Source Host 中断后的静默重启和显式恢复；owner 退出后的 rebind；report 能力隔离；任务运行期间的安装版本 pin 与安全释放。Host 会核对进程 birth 和进程组，未确认退出的树保持 `unknown`，不会提前释放槽或清理工作目录。

[`g1-final-verification.json`](../data/g1-final-verification.json) 归档了全 Go 包测试、`go vet`、并发敏感包 race、生产入口构建、分页/ACK、可信 session 问答和本地 native bridge 测试的已观察结果。该文件是验证结果归档，不是新生成的原始日志。

## G2：三个本机 CLI 的受限适配

G2 把 Claude Code、Antigravity CLI 和 Grok Build 转成统一的启动与结构化事件合同。启动前固定绝对可执行文件、版本和 SHA-256；不接受调用方追加任意参数，不切换账号、provider 或认证配置。来源 Host 继承当前任务的正常来源环境，只移除编排器内部 capability/bootstrap 变量。

结构化 stdout 已接入真实 Host 进程路径：普通诊断有界排空，结果、失败和技术问题按 schema fail closed；Claude/AGY 的 init 和 Grok 的结构化 session 会及时形成可信内部事件。answer resume 必须使用同一 attempt 已持久化的 session。AGY 的 `WAITING` 保留原生问题路径，permission 事件不会被当作技术问题。

适配器、Host parser、session 续接和 report question 的本地 fixture/纵向链路已经通过。尚未执行三个真实 provider 账号的认证模型调用矩阵，因此当前结论不覆盖账号可用性、套餐、模型权限或 provider 服务端行为。

## G3：隔离 Git worktree、候选冻结与安全清理

G3 在来源 Host 中执行 Git，协调器本身不直接运行 Git。它为每个任务创建独立 worktree，用私有 ref 和 expected OID 冻结候选；按依赖顺序 compose；只有审查绑定、目标 ref、HEAD、index 和工作树都匹配时才允许最终 fast-forward integrate。

清理使用 common-dir OS 锁、目录 FD、device/inode 身份和逐项清单。未知新增、symlink、身份漂移、活跃进程或账本不确定都会保留现场并返回 `cleanup_pending`/`unknown`；不会执行全仓 prune、GC、reset、stash 或递归删除未知内容。

真实临时 Git 仓库已经通过同一 binary 的 Source Host `compose`（含 private ref）→ `integrate` → `cleanup` 纵向链路，结果为候选、整合和清理各阶段的持久 receipt。覆盖范围不包括远程 push、Windows 文件占用和恶意同 UID 进程竞争。

## G4：项目内插件包、版本管理与原生桥

G4 把同一 orchestrator binary、Codex plugin、native bridge 和固定的 `runtime/g0` 依赖打成可审查版本包。manifest 记录每个文件的路径、mode 和 SHA-256；固定 Python 只记录绝对路径和 SHA-256，不复制或修改系统解释器。安装器使用 OS 锁、版本 receipt、任务 pin、持久卸载意图和 FD 锚定删除，发现修改或未知文件时保留并报告。

rc2 包已经在项目范围的临时目录完成 package、`InstallPackage`、`Doctor` 和 plugin validator 检查。rc1 的临时版本已成功卸载；rc2 仍作为临时验收安装保留，等待根任务完成完整 native product 用例。这里的“安装通过”只指临时 project-local 生命周期，不代表已永久安装到用户环境。

## 尚未完成或明确延期

| 范围 | 当前状态 | 完成条件 |
|---|---|---|
| 完整 native product | 根任务仍在验收 | 用最终 rc2 distribution 完成 attached owner capability、submit、Source Host、回传、typed ACK、rebind/cleanup 的产品用例，并保存最终 receipt |
| G1 长时资源 | 根任务仍在验收 | 对冻结 binary 完成 `resource-acceptance.py` 的原定 5/10/20 分钟场景；短 smoke 不能代替 PASS |
| 真实 provider 账号矩阵 | 未执行 | 分别使用真实 Claude/AGY/Grok 账号进行授权的模型任务，并验证结果、失败、问题和 resume；当前不以本地 fixture 代替 |
| Windows | 延期 | 独立实现并验证 Windows 进程树、文件锁、worktree 清理与安装语义；编译成功不算验收 |
| 永久安装与激活 | 未执行 | 用户审查最终 package manifest、安装路径、service 配置和验收 receipt 后，单独授权永久安装/激活 |
| 公开发布与许可证选择 | 不在本轮范围 | 需要另行确定发布目标和许可证 |

## 用户可审查的下一步

1. 根任务完成一次完整 native product 验收，并把 rc2 binary/package SHA、安装与 Doctor receipt、native delivery/ACK/rebind/cleanup 结果写入项目证据目录。
2. 根任务使用同一冻结 binary 完成 G1 长时资源场景，逐项保留时长、负载、durable report、RSS、CPU 和趋势结论；任何未跑满场景继续标为未验收。
3. 用户审查上述证据以及 package manifest、永久目标路径和激活配置。只有永久安装/激活需要下一次明确授权；这项授权不包含登录、登出、账号切换、读取或修改任何 CLI auth。

本轮没有永久安装、注册 marketplace、修改全局 Codex 配置、启动真实模型任务或改动本机多个 CLI 的认证状态。测试遗留进程 PID 47082 仍按已有记录保持 `cleanup_pending`；不再向它或归属不明的父进程发送信号。
