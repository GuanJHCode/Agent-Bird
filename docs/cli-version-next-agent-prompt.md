# 当前接手提示词（2026-11-04）

先读[当前交接](cli-version-handoff.md)、[机器验收](cli-version-verification.json)、[源码摘要](cli-version-source-manifest.json)及[本轮进度](../tasks/codex-agy-series/docs/progress.md)。以下是当前状态；旧批次仅追溯，不是待修任务。

2026-11-04 用户恢复验证：当前研发包和公开候选各31项真实闭环均通过。昨日IP绑定/403暂停为历史状态；后续先核对实际HEAD、工作区与摘要，不重复已有效的模型验收。

```text
接手 https://github.com/GuanJHCode/Agent-Bird.git
分支 handoff/cli-version-compatibility-20261102。
先核对实际HEAD、工作区和269项源码摘要，保护现有变更。

当前研发包0.1.22-cli-series：Codex0.154.0–0.166.0全部21发布版精确native准入、
每版6项native证明，以及当前安装包read/marker/size/SHA/collect/ACK/accept/completed
均通过。AGY1.2.3–1.4.12全部35版真实version/help/SHA通过，六代表
1.2.3/1.2.8/1.2.9/1.4.10/1.4.11/1.4.12当前同包真实闭环，29版仅只读合同组推断。
AGY1.3.4固定官方artifact已取得并验证，不再列为缺失。

Go full/race465顶层/992含子例通过、0失败、7skip，vet/build通过；Python172pass/8skip。
Codex合法空中间消息已支持，最新cursor会被空消息清除，空终稿仍拒绝；五字段usage、
session/terminal/oversize不放宽。AGY finished_reason先于response拒绝partial；event:null
诊断修复已回归。pkg19失败和AGY1412首次超时保留，不替代最终接受。

冻结catalog498：Claude405（1.0.61–128、2.0.x、2.1.0–317）按20只读合同组；
Grok1.0.34–70全部37，AGY35、Codex21。Claude快照latest/next2.1.317、stable2.1.311，
运行时不追latest。Claude36版无模型、27代表真实闭环及Grok37真实闭环适用历史pkg17，
其中Claude两端点和Grok两端点也已在pkg22独立重测通过。跨批次129实际二进制、91直接模型版本；369个Claude无二进制检查，
407版无直接模型验证（Claude378+AGY29），不得写通过。逐行状态见version证据。

Codex生产FileLoginSource/只读投影、唯一native guard refresh tripwire、精确main及
CodeMode companion、receipt/helper/interpreter已接线。任何已有override含空/相同值
都拒绝，Go不能提前注入。用户已批准固定摘要native主进程受信，工具由native named
readonly profile约束；不能恢复外层Host sandbox。未知身份保留完整四项missing。

Claude gateway只支持plain typed reviewer/read-only，write/candidate/untyped拒绝。
ExactEnv、新HOME、空strictMCP、不可写policy和原认证/Keychain/securityd隔离保留。
旧JS需显式固定官方Node24.13.0 darwin-arm64，不能恢复PATH fallback或读原HOME修启动。
Claude/Grok网关deepseek-v4.1-flash-1017不代表第一方模型验收。Grok原账户无余额，
不再重试；AGY用户已自行重新登录，六代表通过但绝对剩余额度未知。密钥、私人地址、
个人路径、认证和原session不入Git；不改原全局安装或认证。

公开产品使用本轮public-native选择性补丁及独立源码/包/验收，基于公开main2e78f24；
保留currentOwner/Controller/Dispatch/coding/Claude/Grok/AGY/portable collect-only。
Codex只在full native runtime启用，portable仍collect-only。公开包0.1.21-public-native已独立full/race444/879pass、5skip，vet/build、Python151/1skip、
安装doctor、Codex21+AGY6真实闭环通过。Claude最旧/最新及Grok两端点也在当前公开包独立真实闭环通过；两个包各31项。
昨日IP绑定/403阻塞与暂停保留为历史；不重试原Grok余额，不用pkg17其他版本顶替。
主包和公开候选各自模型结果分开，未测不沿用；未发布，不整体合并，不碰main/master。

剩余：完整Codex工具目录未独立持久化逐项验收；typed resume、write/candidate、native
TUI、其他Node/平台未支持或未验收；cleanupCodexAuth同UID路径竞态后续加固。
仅这些适用缺口及新发现继续处理；已关闭矩阵/Stop-Wait/缺21pins不重新列待修。

后续行为变化补必要回归，Go改后full/race/vet/build，Python guard跑对应测试；稳定
快照独立审查。受影响证据重验，未受影响证据按组件摘要明确复用，更新隔离包摘要链。
沿用交接分支提交/非force推送授权，已授权工作自主推进，仅询问真实缺失预算、权限或
业务决策。不改已有锁/冻结目录。交付分列实现、自动化、实际闭环、内部缺口、账户、
未测版本、公开候选/发布范围、提交与远端回读。最终状态以实际HEAD及机器记录为准。
```

pkg11/pkg17 提示词、旧数量和早期下载/登录阻塞仅见历史记录，不作为当前执行要求。
