# CLI 多版本试用交接

## 当前状态（2026-11-04）

用户恢复验证后，网关从昨日 HTTP403 恢复为 HTTP200。当前研发包与公开候选各自完成 **31 项真实闭环：Codex21、AGY6、Claude2、Grok2**；昨日暂停及失败仅保留为历史，不再是当前账户阻塞。

当前研发隔离安装包为 **`0.1.22-cli-series`**。Codex 0.154.0–0.166.0 的 **21 个已发布版本全部完成精确 native 准入与当前包真实闭环**；AGY 1.2.3–1.4.12 的 **35 个发布版全部完成实际二进制检查，六个合同代表版在同一当前包完成真实闭环**。29 个 AGY 非代表版只有合同组推断，未标为直接模型通过。

冻结目录共 498 个版本，运行时不追踪 latest。Claude 官方快照（2026-11-03）为 latest/next 2.1.317、stable 2.1.311；覆盖 1.0.61–1.0.128、2.0.x、2.1.0–2.1.317 的 405 个发布版。小版本归组只支持受测只读场景。

| CLI | 冻结范围 | 实际二进制无模型验证 | 直接模型证据及适用包 |
| --- | --- | --- | --- |
| Codex | 21 个发布版，0.154.0–0.166.0 | 全部 21；每版另有 6 项真实 native 合同证明 | 全部 21，当前 pkg22 |
| AGY | 35 个发布版，1.2.3–1.4.12 | 全部 35，包含此前下载受阻的 1.3.4 | 1.2.3、1.2.8、1.2.9、1.4.10、1.4.11、1.4.12，当前 pkg22 |
| Claude Code | 405 个发布版，三个系列、20 个合同组 | 36；另过本地 SSE/Read/Bearer，3 个代表版过 API-key 合同 | 当前 pkg22 两端点 1.0.61/2.1.317；另有历史 pkg17 的 27 代表版 |
| Grok | 1.0.34–1.0.70，37 个正式版 | 全部 37 | 当前 pkg22 两端点 1.0.34/1.0.70；另有历史 pkg17 的全部 37 版 |

跨批次共 **129 个版本完成实际二进制检查、91 个版本有直接真实闭环**。仍有 **369 个 Claude 版本未做实际二进制检查；407 个版本未做直接模型验证（Claude 378、AGY 29）**。不得把 catalog 准入、组内推断、无模型探针或旧包成功写成当前包通过。

逐版来源、原始 `--version`、路径别名、main/companion/archive SHA、参数及各层结果见[本轮逐版验收](../tasks/codex-agy-series/docs/verification.json)；Claude/Grok 历史系列见[原逐版记录](../tasks/cli-series-coverage/docs/version-series-verification.json)。当前源码以[269 项摘要清单](cli-version-source-manifest.json)和实际 Git HEAD 为准。原始认证、session、个人路径与失败现场留在仓库外 owner-only 环境。

## 已实现与自动化

- Codex 三份生产表精确对齐 21 对 main/CodeMode companion 摘要；跨 provider/fake/raw alias、未知身份、错摘要继续拒绝。未知身份保留完整四项 `codex_trial_guard_not_ready`。
- Codex 允许协议合法的空中间 `agent_message`，每条 message 都替换最新 cursor；空终稿仍拒绝，不能回用旧 draft。missing/null/non-string、五字段 usage、session/terminal 顺序、oversize 约束保持。
- AGY 的 `finished_reason` 在读取 response 前检查，超时/中断/错误及未知、空、null、非 string 一律拒绝 partial 结果；诊断 `event:null` 不再误记为解析成功。
- 当前 Go full/race 均 **465 顶层／992 含子例通过、0 失败、7 显式跳过**，vet/build 通过。Python **172 通过／8 跳过**。Codex 21 版各 6 项原生证明共 **126 项通过**，与真实模型结果分列。日志摘要、跳过列表和包/receipt/helper/interpreter 绑定见[自动化验收](../tasks/codex-agy-series/docs/automation-verification.json)。
- 当前 pkg22 的 31 次接受均逐项核验 installed package/no bypass、严格匹配 marker 或唯一尾随换行、产物 size/SHA、collect、ACK、accept、最终 completed。AGY 1.4.12 首次等待超时后，同一 150 秒上限有界重试通过；首次失败保留。

当前研发包新网关四端点证据见[当前包网关闭环](../tasks/codex-agy-series/docs/current-gateway-evidence.json)。可运行 `python3 tasks/codex-agy-series/scripts/verify_evidence.py` 离线核对 56 条报告记录的内部一致性（不重算仓库外原生日志或 round 文件）；不会读取认证或请求模型。伪造未测为通过、旧包、错产物或缺 native 证明的记录均已做拒绝检查。

## Codex 四项生产合同

| 合同 | 生产实现 | 验收证据 |
| --- | --- | --- |
| 真实登录态受信只读投影 | `codex_exec.py:FileLoginSource/prepare_launch`；固定 native login status、稳定来源快照 | 缺失、不可信、链接、过期、来源变化、配置/preferences 异常拒绝；21 版 prepare native 证明及当前包真实登录投影 |
| refresh tripwire 唯一注入 | installed helper 调 `auth_guard.py:prepare_guarded_environment`；Go 不注入 | 任何已有 override，包括空值或相同值，在读取 binary/auth 前拒绝；native positive/guarded 对照、auth identity/字节不变 |
| 精确主进程与模型工具权限 | `admit.go:CodexNativePins`、`verify_bundle`、固定 receipt/helper/interpreter、命名只读策略 | 21 对 main+companion 摘要、篡改拒绝；原生工具权限探针，工具网络连接 0 |
| exec/collect 完整接受 | Host、report wrapper Pin、installed native helper、JSONL observer、owner loop | pkg22 全部 21 版 read/marker/size/SHA/collect/ACK/accept/completed |

沿用用户批准的边界：**固定摘要 native 主进程受信，模型工具由原生命名只读 profile 约束**。不恢复导致 native 文件工具二次 sandbox 失败的外层 Host Seatbelt，不在 Go 重复注入。CodeModeOnly companion 禁止 in-process fallback。

## Claude/Grok 与账户条件

Claude 自定义 Anthropic gateway 仅支持 plain typed reviewer/read-only；无 profile、implementer、candidate action/workspace 在分支选择前拒绝。ExactEnv、新 HOME/config、空 strict MCP、不可写 policy、原 HOME/auth/Keychain/securityd 隔离继续保留。旧 JS CLI 显式使用官方 Node v24.13.0 darwin-arm64 固定摘要，不回退 ambient PATH。metadata 同样无密钥隔离并核验 pin。

Claude/Grok 真实网关证据使用用户指定的 `deepseek-v4.1-flash-1017`，不证明第一方模型或任意其他网关兼容。Grok 原账户无余额由用户确认，停止原账户重试；Claude 第一方登录上次检查未登录，本轮不更改。AGY 由用户自行重新登录后当前六代表通过，绝对剩余额度仍未知。外部条件不阻止其他端点和内部实现推进。

## 公开产品与剩余范围

公开产品在独立 public-main 快照上选择性移植，基线 `2e78f24c5e9694f287d19065f2ce9c80b16a59af`；保留 currentOwner、Controller、Dispatch、coding、Claude/Grok/AGY 和 portable collect-only 产品入口。完整 native runtime 才启用 Codex；portable collect-only 不具备该运行时。不得把研发交接分支整体覆盖公开 main/master。

公开候选 **`0.1.21-public-native`** 已在干净 public-main 快照应用 64 文件补丁，243 项源码摘要匹配。Go full/race 均 **444 顶层／879 含子例通过、0 失败、5 跳过**，vet/build 通过；Python **151 通过／1 跳过**。独立安装、doctor、24 文件字节/权限核验，以及 Codex21＋AGY6＋Claude2＋Grok2 当前公开包真实闭环均通过。Codex0.162.1首次等待超时后保持原边界有界重试通过，失败保留。

当前公开包 Claude1.0.61/2.1.317、Grok1.0.34/1.0.70 四端点均通过完整 read/marker/size/SHA/collect/ACK/accept/completed；当前研发包同四端点也已分别通过。两个包各31项，入口与包摘要分开核验。昨日IP绑定导致的403及Grok失败现场保留为历史，未覆盖失败或放宽验收。此前pkg17其他Claude/Grok版本仍只保留历史适用范围。

见[公开补丁](../tasks/codex-agy-series/docs/public-native.patch)、[公开源码及包清单](../tasks/codex-agy-series/docs/public-native-manifest.json)、[公开产品验收](../tasks/codex-agy-series/docs/public-native-verification.json)。公开发布/合并未执行；研发包和公开候选分别绑定源码及包摘要，不能共用模型通过声明。

内部仍有：完整 Codex 注册工具目录未单独持久化逐项验收；typed resume、Codex write/candidate、native TUI、其他 Node/平台未支持或未验收；`cleanupCodexAuth` 同 UID 路径竞态是在现有 0700 Host-owned 边界下的后续加固项。连续小版本推断限于记录的只读协议合同，不扩大成功范围。

## 历史批次（仅追溯）

- `d7bc3d9`、`55741e2`：420／816、430／834，均 6 跳过；矩阵诊断和 Stop/Wait 合同后续已关闭，不再列为待修。
- pkg11：441／856、6 跳过；139 catalog、13 无模型、10 真实闭环，见[历史记录](../tasks/cli-production-rollout/docs/verification-pkg11.json)。
- pkg17（`7824e05`）：454／889、7 跳过；68 个直接闭环，包含 Claude27/Grok37/AGY2/Codex2。它不是修改后的 pkg22 验收。
- pkg18/19 为中间验证；pkg19 的 0.155.0 失败揭示合法空中间消息，已修复及回归。旧 AGY 1.3.4 无可核验 artifact、Codex19缺准入、公开产品完全无 native 接线等结论均已过时。

沿用提交和交接分支非 force 推送授权；不操作 main/master，不改全局安装、认证、已有锁或冻结目录。当前任务完成状态见[进度记录](../tasks/codex-agy-series/docs/progress.md)，提交与远端回读以机器验收及实际 Git 为准。
