# 当前恢复入口

先核对本目录[当前交接](final-handoff.md)和[TASKS](../TASKS.json)。当前package source为已推送 `7eb8c8d434a584fd109d9bed96d8c3a82f3fea98`；唯一私有 `.5` PKG、同包REG及无模型bootstrap已独立/rootPASS。当前文档独审/root资料签收PASS，handoff为PREPARED；产品core/T12/T13与nativepush仍BLOCKED，真实 `.5` LIVE NOT_RUN。

当前私有归档在 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-package-r5/artifacts/`；包archive5ad3caec…、binary6b1d91d5…，完整身份见[PKG root](../data/public-evidence/T11-package-r5-root.json)。保留全部旧包/失败/预算，不重标.4、不重跑旧.3 launcher或聊天key loader；不再构建.5。

下一步在资料签收后确认新的有界真实试验范围：两个方向严格串行，每方向parent180秒含cleanup、worker60秒/总90秒/最多2attempt，合计最多6分钟wall，Codex gpt-6-luna/low，Claude当前CC token/Bearer及原默认Deepseek，不换账号/模型、不增加付费探针。旧试验时限已经结束，当前准备PASS不是新的模型消耗授权。

已实际准备的新trial `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t12-live-r5-20261109`；root-only launcher在相邻 `t12-r5-live-prep/scripts/final_trial.py`，只从当前有效settings/CC Switch内存取源，禁止复制密钥或旧loader。51pins、7scripts、安全串行gate和两固定合同见[bootstrap](../data/public-evidence/T12-bootstrap-r5-root.json)与[独审](../reviews/T12-live-preparation-r5-review.md)。当前未消费，任何live-run/started/completed均不存在；即使后续失败也不删除marker或重置方向。

获得本轮明确范围后root执行：先codex-to-claude，通过完整owned cleanup/ledger/accept/ACK且fresh finish复验后才claude-to-codex；两个方向结束再用原FF机制整合私有fixture，不能代替真实父验收。source/env/pins变化、第一方向失败或清理未知均停止，不继续另一方向。保留typed resume/native push关闭。

历史过程见[恢复历史](resume-history-20261105-20261109.md)、[旧live](../data/public-evidence/T12-live-root.json)及各连通性报告。当前nonce连通已成功，六字段来源ENV修复已完成；不要再把旧401/403或旧模型缺口当当前阻塞，也不要据此宣称产品LIVE已通过。
