# Codex / AGY 连续系列闭环

## 当前里程碑

- 研发包 `0.1.22-cli-series` 已接受：Codex 21 版逐版、AGY 六合同代表，加上恢复后Claude2/Grok2，合计31个当前包真实闭环；AGY 35 版全部取得官方固定 artifact 并完成 version/help/参数/SHA 检查。
- Codex 三表精确准入 21 对 main/companion，21×6=126 原生合同证明通过。guard 唯一注入 tripwire；已有 override 含空/相同值拒绝；未知身份保留四项 missing。原生命名只读工具策略沿用用户明确授权。
- AGY partial finished_reason 拒绝、诊断 event:null、Codex 空中间 message/cursor 与 fake alias 拒绝已实施并补回归。没有重新修改已关闭的矩阵/Stop-Wait 生产合同。
- Go full/race：465 顶层／992 含子例通过，0 失败、7 显式 skip；vet/build 通过。Python 172 通过、8 skip。独立代码和最终研发包证据审查通过。仅文档更新后的重建 binary 与 pkg22 原 SHA 完全相同。
- 当前源码摘要清单 269 项；逐版与包证据分别见 verification.json、automation-verification.json。离线 checker 通过，四类伪造记录均被拒绝。
- 公开产品独立树选择性移植 full-native Codex 与兼容能力，保留 currentOwner/Controller/Dispatch/coding/portable collect-only；公开包0.1.21-public-native已独立full/race444/879通过、5skip、vet/build通过、Python151通过/1skip；Codex21+AGY6同公开安装包真实闭环全部接受。恢复后Claude2/Grok2同公开包也通过，合计31项真实闭环。昨日403失败保留为历史。结果单独记录，不与pkg22混用。

## 关键决策与归属

- 目标为 Codex 0.154.0–0.166.0 共 21 发布版、AGY 1.2.3–1.4.12 共 35 发布版。AGY 按 CLI 协议形状及已核验边界分三组，六锚点直接模型验证，29 版只读范围推断，不标直接通过。
- 主会话唯一写入研发 worktree；公开候选在独立 worktree，生产代码冻结后独立审查。原始下载/测试/模型现场在仓库外 owner-only 目录。
- 不改全局安装、认证、已有锁、冻结目录，不操作 main/master。沿用交接分支提交及非 force 推送授权。
- Grok 原账户无余额由用户确认；Claude/Grok 使用指定网关模型，第一方模型未验收。AGY 用户自行重新登录后当前六锚点通过，绝对剩余额度未知。

## 保留的开发失败与纠正（历史）

- AGY 1.3.4 原官方页面读取失败曾被误记无 artifact；已通过固定官方 asset 取得并核验，当前不是缺口。
- AGY 登录曾过期，用户已在原客户端完成重新登录；未通过测试修改认证。pkg22 的 1.4.12 首次超时保留，同一 150 秒上限仅重试该一版后完整接受。
- pkg19 Codex 0.155.0 真实失败发现合法空中间 agent_message；TDD 红后修复，最终 pkg22 全21通过。空终稿仍拒绝，usage 五字段未放宽。
- 下载 fork 压力曾使既有 2 秒 probe 夹具波动；暂停下载、串行协调后完整验证通过，未改生产超时。私有 runner 的 077 umask 曾影响权限负例，测试子进程使用项目默认 022，runtime 安全模式未放宽。
- 诊断 import 在自有 pkg19 产生的两个额外 pyc 已精确清除，24 个原安装文件未变；后续 helper 检查使用禁止字节码方式。
- 基线 `7824e05`／pkg17 为历史：261 摘要、68 直接模型闭环。430／834、6 skip 等更旧基线不代表本次修改后结果。

## 尚未覆盖

完整 Codex 工具注册表单独持久化审计；typed resume、write/candidate、native TUI、其他 Node/平台；cleanupCodexAuth 同 UID 路径竞态后续加固。Claude 369 个版本无实际二进制检查、378 个无直接模型验证；AGY 29 个无直接模型验证。公开发布/合并未执行。

## 交付收尾

- 公开补丁已在干净public-main快照应用，243项源码摘要匹配；64文件选择性差量，不覆盖main/master。
- Grok1.0.70/Codex0.166.0 ceiling固定路径已在任务私有env配置并重新核验实际摘要，公开记录只含安全路径别名。
- 当前handoff/next-agent-prompt/机器聚合已整理，已关闭问题和旧包状态不再作为当前待修。最终隐私扫描、提交与远端回读按交付实际结果收尾。

## 历史用户暂停（2026-11-03，已恢复）

用户确认网关key绑定IP，要求先暂停、明天处理。已停止所有模型请求；不调整IP白名单、权限或认证。当前研发包Codex21+AGY6及公开包Codex21+AGY6证据保留。公开包最终独立证据复审、完整隐私扫描、提交及交接分支远端回读尚未完成。源码未再变更，HEAD仍为7824e05；本批修改保留在工作区。下次先核对实际HEAD/工作区/269摘要，再从本节续接，不重跑已有效的验收。

## 2026-11-04 恢复验收

用户要求重试后，最小OpenAI请求HTTP200。公开pkg21及研发pkg22分别重验Claude1.0.61/2.1.317、Grok1.0.34/1.0.70，八个新round全部严格接受；两个包各31项真实闭环。源码269摘要及模式完全匹配，因此复用不受影响的Go/Python/native证据，不重跑模型或旧测试。公开候选27项native证据独立复审通过，新增八个gateway round另行复审。昨日暂停、403和超时保留为历史。当前状态以机器聚合及实际Git为准。

新增八个gateway round独立证据复审通过；最终扫描0泄漏，临时网关密钥副本已移除，固定binary/原登录和权限边界保持。交接分支按既有授权提交/非force推送；具体commit与远端读回以实际Git交付结果为准。
