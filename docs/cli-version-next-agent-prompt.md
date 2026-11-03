# 当前接手提示词（2026-11-03）

当前连续系列、生产接线、自动化和真实闭环见[当前交接](cli-version-handoff.md)、[机器验收](cli-version-verification.json)和[逐版记录](../tasks/cli-series-coverage/docs/version-series-verification.json)。以实际 HEAD、源码摘要和本轮验收记录为准；历史批次仅供追溯。

```text
接手 https://github.com/GuanJHCode/Agent-Bird.git
分支 handoff/cli-version-compatibility-20261102。
先核对实际HEAD、工作区和docs/cli-version-source-manifest.json每项摘要，保护已有改动。
读取当前handoff/verification及tasks/cli-series-coverage/docs/progress.md和逐版证据。

当前包0.1.17-cli-series：Claude三个系列共405发布版（1.0.61–128、2.0.x、2.1≤317），
按20合同组覆盖，36固定版无模型/真实CLI合成合同、27代表版真实闭环；其余378没有
直接模型验证，不能标通过。Grok1.0.34–70的37个正式版均在当前包逐版完整闭环。
Codex0.154/0.166和AGY1.2.3/1.4.12当前包闭环通过，总68真实版本。
官方快照latest2.1.317/stable2.1.311；运行时不追latest。目录版本不等于实际二进制
或模型通过。未发布的版本号、pre-release和超窗版本继续拒绝。

Codex生产只读投影、唯一native guard tripwire、精确main+companion pin已实现。
用户已批准固定摘要native主进程受信，模型工具走原生命名只读profile；不要恢复外层
Host sandbox，不在Go重复注入，不删guard或缩短未知身份的四合同missing清单。
当前19个Codex目录版本未获native准入；0.159.2/0.165.0只过metadata，不能写模型通过。

Claude自定义gateway仅plain typed reviewer/read-only；candidate/write/untyped拒绝。
ExactEnv、新HOME/空strictMCP/不可写policy、原认证/Keychain/securityd隔离已落地；
metadata也无密钥隔离并校验pin。旧JS CLI必须显式ORCHESTRATOR_CLAUDE_NODE_BINARY，
仅接受官方Node24.13.0 darwin-arm64固定摘要，不能恢复PATH fallback或读原HOME修启动。
原全局安装/认证不改。源码fixture的metadata常量入口不能依赖worker环境，也不能
因race测试binary的人工退出sleep而放宽生产probe时限或环境。

真实Claude/Grok使用用户网关和deepseek-v4.1-flash-1017，不代表第一方模型验收；
密钥/原网关地址/个人路径/原始session不入Git。Grok原账户0余额由用户确认，不重试；
AGY有当前成功但绝对剩余额度未知。首次AGY格式失败保留，不把它误报quota错误。
已有授权足够的工作自主推进，仅问确实缺少的预算/权限/业务决策。

剩余版本分层状态逐行查新证据，不沿用旧126/129待测数量；现419个无实际二进制
检查、430个无直接模型验证（包含Claude按组推断）。AGY1.3.4尚无可核验官方artifact。
完整Codex工具目录、native TUI和公开产品原生安装/模型验收仍未完成。

公开产品只使用新public-series.patch及其manifest/verification，基于main2e78f24。
38文件选择性移植保留controller/dispatch/reconcile/coding/portable；公开Codex原四项
门禁保留，未移植研发native principal。未发布，不整体合并交接分支，不碰main/master。

后续行为修改补必要回归；Go改后full/race/vet/build，Python guard变化跑对应测试；
稳定快照独立审查，受影响证据重验，未受影响证据按组件摘要明确复用。更新隔离包与
摘要链。沿用交接分支提交/非force推送授权，无需重复审批。不改已有锁/冻结目录。
矩阵和process Stop/Wait合同已关闭，不重新列为待修。

交付列清已实现、自动化、真实闭环、内部缺口、账户条件、未测版本、公开候选/发布
边界、提交和远端回读，不能用fixture或旧包成功替代当前实际接受。
```

旧pkg11提示词和批次数量仅保留在历史交接记录，不作为当前执行任务。
