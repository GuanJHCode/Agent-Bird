# 当前接手提示词（2026-11-03）

Codex 生产只读链路、native guard 独占注入、受信投影和两个精确 native pin 已实现。最终隔离包与真实闭环、自动化数量见 [当前交接](cli-version-handoff.md) 和 [机器验收记录](cli-version-verification.json)。当前实际提交以交接分支 HEAD 和源码摘要为准；`55741e2` 是历史基线，不是继续修复旧矩阵/进程问题的指令。

```text
接手 https://github.com/GuanJHCode/Agent-Bird.git
分支 handoff/cli-version-compatibility-20261102。
先核对实际 HEAD、工作区、docs/cli-version-source-manifest.json 的每项摘要；
保护已有改动。读取 cli-version-handoff.md、cli-version-verification.json、
tasks/cli-production-rollout/docs/progress.md 和 version-inventory.json。

已完成的生产实现不重新立项：Codex 0.154.0/0.166.0 精确 main+companion pin、
安装 receipt/helper/interpreter 校验、真实登录态只读投影、native guard 唯一
refresh tripwire 注入、来源/过期/变化/任何已有override拒绝、Host exec/collect。
用户已批准固定摘要native主进程受信，由原生命名只读profile约束模型工具；
不要恢复会导致工具二次sandbox失败的外层Seatbelt路线，也不要在Go重复注入。

按当前验收记录确认哪些版本已测，分别保留版本准入、无模型、协议解析、真实
闭环状态。Claude/Grok当前成功使用用户提供的网关和deepseek-v4.1-flash-1017，
不等于第一方Claude/xAI模型验收。密钥与网关原地址仅在私人会话/临时环境中，
不得复制入Git或全局配置。Grok原账户无余额已由用户确认，禁止无目的重试。
AGY当前包两端点成功不代表已量得绝对剩余额度。

剩余：126个版本无实际二进制验证、129个版本无真实模型验收；Codex0.159.2/
0.165.0只通过无模型检查，缺精确native验收仍保留完整四合同门禁；AGY1.3.4
未取得可核验官方固定artifact。按实际需要推进固定版，不追踪运行时latest。
完整注册工具目录快照、native TUI与公开产品原生安装/模型验收尚未完成。

公开产品选择性移植补丁已独立实现、审查和自动化验收，见本任务docs中的
public-product.patch及verification；基于公开main2e78f24，未发布。
不可整体合并研发快照或覆盖公开controller/dispatch/coding/portable入口。
不操作main/master；沿用既有交接分支提交/非force推送授权。
不改全局安装、认证、已有锁或冻结目录，不扩大权限。

后续代码行为变化补必要回归，Go行为变化做全量/race/vet/build；Python guard
变化跑对应测试。稳定快照独立审查，受影响证据重验并更新包与摘要链。
未受影响证据可以按组件摘要明确复用。旧430/834与旧包AGY成功不代表当前验收。
矩阵三项及process Stop/Wait合同已经关闭，不能重新列为待修。

交付逐项列出已实现、自动化、真实闭环、内部缺口、账户条件、未测版本、
公开产品候选/发布范围、提交与远端回读；不得以fixture或未测项冒充通过。
```

旧批次提示词已被本节替换；历史批次保留在交接文档的历史区。
