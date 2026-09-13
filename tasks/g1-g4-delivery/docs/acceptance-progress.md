# G1–G4 最终功能与验收范围

本轮 G1–G4 项目内实现与验收已收口：G1–G3 本地功能链、G4 rc6 临时安装生命周期、私有原生产品链和全部 G1 核心资源场景通过。没有执行完整 G0/G1 联合长测或真实 provider 账号矩阵，不能把这些范围写成已验收。

## G1：本地任务调度与持久状态

G1 是调度中枢。它接收当前 Codex 任务提交的计划，以最多两个活动槽执行依赖图，并把任务、attempt、segment、预算、问题、结果、停止状态和待回传事件写入持久状态库。后续查询和操作使用 owner-only 的 `control_file`，不会只凭 task ID 跨任务操作。

已经通过的范围包括：双槽和依赖释放；分页 `collect`、`wait-events`、typed ACK；技术问题停止旧 segment 后在同一 attempt 携带可信 provider session 继续；显式 retry、accept、answer、stop、resume；Source Host 中断后的静默重启和显式恢复；owner 退出后的 rebind；report 能力隔离；任务运行期间的安装版本 pin 与安全释放。Host 会核对进程 birth 和进程组，未确认退出的树保持 `unknown`，不会提前释放槽或清理工作目录。

[`g1-final-verification.json`](../data/g1-final-verification.json) 归档了全 Go 包测试、`go vet`、并发敏感包 race、生产入口构建、分页/ACK、可信 session 问答和本地 native bridge 测试的已观察结果。该文件是验证结果归档，不是新生成的原始日志。

## G2：三个本机 CLI 的受限适配

G2 把 Claude Code、Antigravity CLI 和 Grok Build 转成统一的启动与结构化事件合同。启动前固定绝对可执行文件、版本和 SHA-256；不接受调用方追加任意参数，不隐式切换账号或认证配置，provider fallback 只能使用任务预先授权的列表。来源 Host 继承当前任务的正常来源环境，只移除编排器内部 capability/bootstrap 变量。

结构化 stdout 已接入真实 Host 进程路径：普通非 JSON 原始诊断持续排空并有界丢弃；结构化结果、失败和技术问题仍按 schema fail closed。Claude/AGY 的 init 和 Grok 的结构化 session 会及时形成可信内部事件。answer resume 必须使用同一 attempt 已持久化的 session。AGY 的 `WAITING` 保留原生问题路径，permission 事件不会被当作技术问题。

适配器、Host parser、session 续接和 report question 的本地 fixture/纵向链路已经通过。尚未执行三个真实 provider 账号的认证模型调用矩阵，因此当前结论不覆盖账号可用性、套餐、模型权限或 provider 服务端行为。

## G3：隔离 Git worktree、候选冻结与安全清理

G3 在来源 Host 中执行 Git，协调器本身不直接运行 Git。它为每个任务创建独立 worktree，用私有 ref 和 expected OID 冻结候选；按依赖顺序 compose；只有审查绑定、目标 ref、HEAD、index 和工作树都匹配时才允许最终 fast-forward integrate。

清理使用 common-dir OS 锁、目录 FD、device/inode 身份和逐项清单。未知新增、symlink、身份漂移、活跃进程或账本不确定都会保留现场并返回 `cleanup_pending`/`unknown`；不会执行全仓 prune、GC、reset、stash 或递归删除未知内容。

真实临时 Git 仓库已经通过同一 binary 的 Source Host `compose`（含 private ref）→ `integrate` → `cleanup` 纵向链路，结果为候选、整合和清理各阶段的持久 receipt。覆盖范围不包括远程 push、Windows 文件占用和恶意同 UID 进程竞争。

## G4：项目内插件包、版本管理与原生桥

G4 把同一 orchestrator binary、Codex plugin、native bridge 和固定的 `runtime/g0` 依赖打成可审查版本包。manifest 记录每个文件的路径、mode 和 SHA-256；固定 Python 只记录绝对路径和 SHA-256，不复制或修改系统解释器。安装器使用 OS 锁、版本 receipt、任务 pin、持久卸载意图和 FD 锚定删除，发现修改或未知文件时保留并报告。

原生桥以固定大小块计算可执行文件摘要，保留完整 SHA-256 和读前读后身份校验。`bootstrap_timeout_seconds: 300` 只约束启动；进入 watching 后持续到任务终态或 owner 离开，单次 `wait-events` 不超过 30 秒，owner decision 不再共享五分钟总期限。死 driver 会明确报告 `driver_dead/recoverable`，不会伪报仍在 watching，也不会自动恢复任务执行。

rc6 包已经在项目范围的临时目录完成 pinned Go build、package、`InstallPackage`、前后两次 `Doctor` 和 plugin validator 检查。[`package-rc6-verification.json`](../data/package-rc6-verification.json) 记录 binary SHA-256 `f258ab25f5fd8eafa8e08fddc28d630bb342378f7c9fa65cd8c259d6da8b28c4`、installed manifest SHA-256 `5583bc69b4b761e6f36ca2848029ae7b12860a1e2bc772ebcd17451075bd7992` 和 runtime manifest 身份。[`rc6-archive-receipt.json`](../data/release/rc6-archive-receipt.json) 记录归档 `codex-orchestrator-0.1.0-rc6.tar.gz` 为 6,810,308 bytes，SHA-256 `cc0043af6900a3c917a72931371be918e37897a5764c020f2f052de2b4cf1e41`，其中 package manifest SHA-256 为 `40c190c8937367b83412e1cfa8e6f3e069d3169924b8b02ac6d3b39007a6d8e9`。

[`native-product-10/acceptance.json`](../data/cases/native-product-10/acceptance.json) 的完整产品链状态为 `pass`：真实 native owner 建立 attached capability；旧 owner 退出后 Source Host 停止；新 owner 在同一 native profile/thread 上零 turn 静默恢复，完成 owner rebind 后才显式 resume；结果通过 native history proof 和 typed ACK 回传。14 个 auth 路径比较保持不变，没有外部模型调用。[`root-postflight.json`](../data/cases/native-product-10/root-postflight.json) 独立核对了 15 个记录进程身份全部消失，公共 socket 也已移除。同一产品时间线的两个单 Host 采样为 120.421875 MiB 和 122.9375 MiB，低于该 case 的 128 MiB 检查；它们不是持续的 full G0/G1 联合资源基准。

native-product-10 完成后，rc6 的任务 pin 已自动释放。根任务再次确认 `Doctor` healthy 后成功卸载临时 rc4、rc5 和 rc6；当前临时安装根只保留 rc3 及其 `native-product-task` pin，因为它仍对应旧的可恢复失败任务，没有被强制解除。rc6 源码包、archive receipt 和 tar 继续保留在 `data/release` 作为交付物，这不等于 rc6 仍处于安装状态。

以上结论只覆盖项目内临时安装和 native product 验收，不代表永久安装、plugin 全局注册或生产 service admission 已获授权或实现。永久部署属于独立可选范围，条件见 [`permanent-install-activation-proposal.md`](permanent-install-activation-proposal.md)。

## G1 核心资源结果

原始结果、采样、失败根因和跨版本复用依据见 [`resource-acceptance-summary.json`](../data/resource-acceptance-summary.json)。通过场景没有强杀，也没有匹配的产品进程遗留；不重复运行未受修复影响的旧场景。

| 场景 | 实际时长 | 峰值 RSS | 结果 |
|---|---:|---:|---|
| 无任务 | 40.44 秒 | 17.27 MiB | 30 秒后退出 |
| 离线待回调，G1 核心 | 301.75 秒 | 32.09 MiB | CPU 0.248%，持久化 0.098 秒，结果保留 |
| 四个 Host、两个执行者 | 600.84 秒 | 103.70 MiB | 第五 Host 延后，两执行者均跑满 600 秒 |
| 合法输出洪峰 | 1201.11 秒 | 64.64 MiB | 240/240 报告持久化，每执行者 40 个巨行 |
| 未知原始输出洪峰 | 1201.22 秒 | 65.30 MiB | 240/240 报告持久化，每执行者 40 个巨行 |

两种洪峰后半段内存增长斜率分别为 0.13058、0.04464 MiB/分钟，均通过既定门槛。callback 的 attached delivery 及完整安装包联合长测仍为 `NOT_COVERED`。

## 未覆盖或明确延期

| 范围 | 当前状态 | 完成条件 |
|---|---|---|
| full G0/G1 联合长测 | 未覆盖 | native product 中两个单 Host 内存帧只用于同时间线门禁，不能替代持续联合资源测量 |
| 真实 provider 账号矩阵 | 未执行 | 分别使用真实 Claude/AGY/Grok 账号进行授权的模型任务，并验证结果、失败、问题和 resume；当前不以本地 fixture 代替 |
| Windows | 延期 | 独立实现并验证 Windows 进程树、文件锁、worktree 清理与安装语义；编译成功不算验收 |
| 永久部署 | 后续可选范围 | 不属于当前 project-local G4；需先独立解决按连接绑定既有 native profile/backend 的 production admission，并选择、实现和验证 service/plugin 部署方式。当前不请求授权 |
| 公开发布与许可证选择 | 不在本轮范围 | 需要另行确定发布目标和许可证 |

## 用户可审查的下一步

1. 查看 rc6 package identity、native-product-10 receipt 和最终 G1 核心资源结果；完整 G0/G1 联合长测仍未覆盖。
2. 本轮不请求永久部署授权。未来若另行选择永久部署，应先完成独立 production admission 和部署合同，再以新的固定版本/hash/path 提案请求批准；该可选范围不能默认包含登录、登出、账号切换、读取或修改 CLI auth。

本轮没有永久安装、注册 marketplace、修改全局 Codex 配置、启动真实模型任务或改动本机多个 CLI 的认证状态。原有两个 `g0-pending-resolution` worktree、stash `ba919f705c87333d295b492927cbac683508fa09` 和用户对 `proxy_native_runtime.py` 的修改均保持原样。测试遗留进程 PID 47082 仍按已有记录保持 `cleanup_pending`；不再向它或归属不明的父进程发送信号。
