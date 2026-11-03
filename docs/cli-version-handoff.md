# CLI 多版本试用交接

## 当前交付（2026-11-03）

当前隔离安装包为 `0.1.17-cli-series`。用户要求的连续发布系列已配置，运行时不追踪 latest。Claude 官方 npm 快照为 **latest 2.1.317、stable 通道 2.1.311**；支持范围是 **1.0.61–1.0.128、2.0.x、2.1.0–2.1.317** 的 405 个已发布版本，按 20 个合同组记录依据。小版本归组只用于只读场景推断，未直接测试的版本不标为通过。

| CLI | 冻结目录 | 实际二进制无模型验证 | 当前包真实闭环 |
| --- | --- | --- | --- |
| Claude Code | 405 个已发布版本，三个系列 | 36；均另过本地 SSE/Read/Bearer 合同，3 个代表版另过 API-key 模式 | 27 个合同代表版，包括最旧 1.0.61、最新 2.1.317、2.1.275/304 已知网关回归点 |
| Grok | 1.0.34–1.0.70，37 个正式版 | 全部 37 | **全部 37**，逐版在当前包通过 |
| AGY | 1.2.3–1.4.12，35 个发布版 | 2 | 1.2.3、1.4.12 |
| Codex | 0.154.0–0.166.0，21 个发布版 | 4 | 精确 native 0.154.0、0.166.0 |

共 498 个 catalog 版本；79 个实际二进制无模型验证，419 个未做实际二进制检查；**68 个版本在当前包完成真实读取、marker 精确匹配、产物 SHA/size、collect、ACK、accept、final completed，430 个未做直接模型验证**。Claude 其余 378 个版本是合同组覆盖推断，没有记为模型通过。AGY/Codex 未改变的 metadata 检查按固定字节和组件明确复用，当前包模型闭环已重做；历史包成功不代替本轮。

逐版来源、原始 `--version`、路径别名、SHA、参数、合同组和各层状态见[连续版本验收](../tasks/cli-series-coverage/docs/version-series-verification.json)，逐项严格接受证据见[当前包闭环](../tasks/cli-series-coverage/docs/real-series-evidence.json)。原始 session、个人路径、认证、网关地址保留在仓库外 owner-only 环境。可用 `python3 tasks/cli-series-coverage/scripts/verify_evidence.py` 离线核对公开证据，不请求模型。

Go 最终 full 与 race 均为 **454 顶层／889 含子例通过、0 失败、7 显式跳过**，14 个测试包通过，vet/build 通过；新日志和摘要见[机器记录](cli-version-verification.json)。新增 native Claude 合同默认显式跳过，36 个固定版本和 3 个认证模式补充检查已另行执行。Python guard 没有变化，159 通过／4 跳过及两个 native 版本专项按组件摘要复用。初次 full/race 暴露的旧测试夹具问题、最终修正与复验均保留，未减少生产断言或放宽权限。

## Claude 生产只读网关

- 1.0.61 是必要合同下限；更早版本缺少本链需要的 settings 等能力，不能以小版本差异忽略。1.0.61–85 使用 `--disallowedTools`，86 起用 `--disallowed-tools`；不再强制本链不消费的 partial-message 参数。
- 自定义 Anthropic gateway 仅支持 plain typed reviewer/read-only。无 profile、implementer、candidate action/workspace 在分支选择前拒绝。现有第一方/native 产品入口保留；本次网关验收不证明那些入口的认证或模型兼容。
- 真实 worker 使用新 HOME/config、ExactEnv、空 strict MCP、不可写 policy；拒绝 OAuth/双 key/provider 冲突、项目或 managed 配置。OS 拒原 HOME/auth/Keychain、securityd、工作区写入；工具禁 shell、写入、delegation、网络工具及 MCP，提供 `--tools` 时限定 Read/Glob/Grep。实际 Read、认证 header、原认证拒读、policy 拒改及 Mach 正负对照已验证。
- metadata 同样执行前后核验 CLI pin，使用无密钥私有 HOME、禁网/禁全局写的沙箱。JS metadata 单次有界 8 秒，version/help 总窗 20 秒；其他 provider 原 2 秒窗口不变。
- 旧 JS CLI 还需要显式 `ORCHESTRATOR_CLAUDE_NODE_BINARY`，指向规范绝对路径的官方 **Node v24.13.0 darwin-arm64**。代码固定其摘要，拒绝 ambient PATH 或假 Node；probe/launch 复制同摘要字节到私有目录，原安装不改。官方 archive、SHASUMS 和本机字节对照见连续验收记录。没有该固定运行时的其他平台/Node 版本未验收。

Claude/Grok 真实闭环请求用户提供的网关和 `deepseek-v4.1-flash-1017`，不等于第一方 Claude/xAI 模型验收，也不泛化为任意第三方网关的兼容承诺。API-key 401 的历史现场、2.1.275/304 官方已知问题与当前受测服务的直接通过分开记录。

## Codex 四项合同与当前包证据

| 合同 | 生产实现 | 验收依据 |
| --- | --- | --- |
| 受信认证来源 | `codex_exec.py:FileLoginSource/prepare_launch`，固定 native login status 与稳定来源快照 | 缺失、不可信、链接、过期、来源变化、配置/preferences 异常拒绝；当前包真实登录投影 |
| refresh tripwire 唯一注入 | installed helper 调 `auth_guard.py:prepare_guarded_environment`；Go 不注入 override | 任何已有 override（包括空值或相同值）拒绝；固定 native 合成 positive/guarded 对照，auth 字节/identity 不变 |
| 精确 native 与工具权限 | `admit.go:CodexNativePins`、`verify_bundle`、receipt/helper/interpreter，命名只读策略 | main+CodeMode companion 固定摘要、篡改拒绝；实际工具 OS 权限探针，工具网络连接 0 |
| exec/collect 闭环 | Host、report wrapper pin、installed native helper、JSONL observer、owner loop | 当前 pkg17 两端点精确读 TOKEN、产物 SHA/size、collect/ACK/accept/completed |

用户已批准：**固定摘要 native 主进程受信，由原生命名只读 profile 约束模型工具**。不恢复会让工具二次 sandbox 失败的外层 Host Seatbelt，不在 Go 重复注入。CodeModeOnly companion 禁止 in-process fallback。未知身份继续保留完整四项 `codex_trial_guard_not_ready`，没有缩短清单。

## 账户、剩余缺口与公开产品

- Grok 原账户无余额由用户确认，已停止该路线模型重试；新网关路线通过。Claude 第一方登录上轮检查未登录，本轮未重做登录；网关 Bearer 路线通过。AGY 当前包两端点通过，绝对剩余额度仍未知。
- AGY 首次 1.2.3 回答多出空行，严格产物验证拒绝；失败保留，未做 marker 截取或容错放宽。随后 ceiling/baseline 有界重验均完成完整闭环，不能把这次格式失败写成额度错误。
- Codex 19 个目录版本缺精确 native 合同与真实闭环，0.159.2/0.165.0 只有无模型检查；AGY 33 个版本未实测，其中 1.3.4 未取得可核验官方固定 artifact。Claude 369 个版本无实际二进制验证、378 个无直接模型验证。
- 完整 Codex 注册工具目录尚未单独持久化逐项验收；typed resume、Codex write/candidate、native TUI、其他 Node/平台仍不支持或未验收。`cleanupCodexAuth` 的同 UID 路径竞态是现有 0700 Host-owned 边界下的后续加固项。
- [公开产品候选](../tasks/cli-series-coverage/docs/public-series.patch)基于公开 main `2e78f24c5e9694f287d19065f2ce9c80b16a59af`，选择性移植 38 文件；保留 controller/dispatch/reconcile/coding/portable，公开 Codex 仍为原四项门禁。最终证据见[独立产品验收](../tasks/cli-series-coverage/docs/public-series-verification.json)。**未发布，未做公开产品原生安装/模型验收，main/master 未更新**；不能整体合并研发交接快照。

## 历史批次（不再是待修清单）

- `d7bc3d9`：420／816、6 跳过；后续矩阵诊断与 Stop/Wait 合同已关闭。
- `55741e2`：430／834、6 跳过，225 项摘要；不是当前修改后的结果。
- `2e089338`／pkg11：441／856、6 跳过；139 catalog、13 无模型、10 真实闭环。完整[历史机器记录](../tasks/cli-production-rollout/docs/verification-pkg11.json)与旧任务证据保留。
- pkg12–16 为本轮开发/失败或局部验证包；早期 pkg13 Grok37成功和 pkg15 Claude latest 成功保留，当前接受统一使用 pkg17。

沿用提交及交接分支非 force 推送授权，不操作 main/master，不改全局安装、认证、已有锁或冻结目录。实际提交和工作区以 Git HEAD、[源码清单](cli-version-source-manifest.json)及[当前任务记录](../tasks/cli-series-coverage/docs/progress.md)核对。
