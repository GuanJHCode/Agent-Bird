# 当前恢复入口

**本轮已完成：私有`.8`双向真实CLI、候选验证、父accept/ACK、私有fixture FF、预算与进程清理PASS。** 详见[最终验收](../../claude-call-acceptance-20261109/docs/final-acceptance.md)、[当前交接](final-handoff.md)与[完整原始证据](../../claude-call-acceptance-20261109/closure/data/final-result.json)。最终独立证据签收PASS，见[最终审查](../../claude-call-acceptance-20261109/docs/final-review.md)。

最终trial `/private/tmp/ab8-1109-r8`，两方向均已消费并完成；不要重置marker或重跑真实调用。最终包identity在 `tasks/claude-call-acceptance-20261109/revisions/r7/tmp/package-r8/data/identity.json`，runner在同任务`revisions/r8/scripts/final_trial.py`。两方向wall87.395/109.344秒，worker19,375/81,961ms，各1attempt，active/unknown/reserved/pending归零，hostoffline。

实际修复工作区 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration`，分支`feat/cross-cli-hardening-20261105`。`.8`构建时为`ed3c8060`加dirty patch，包身份保留原source-materials/patch；本提交收录对应修复与验收记录，用户随后明确授权推送，见[交付说明](../../claude-call-acceptance-20261109/docs/publication.md)。全局`.bird1/1.0.0-rc.4`未替换，原用户cwd旧分支及其未跟踪文件保留。

代码修复：Claude两个启动时本机IPC字段从provider来源认证指纹中精确排除；Codex固定0.166标准bundle严格校验manifest、目录权限及四可执行文件SHA。来源auth文件仍严格0600，认证/路由变化与未知字段仍拒绝。验收指令修复短socket路径、ACK正确tuple、可选task_id复制错误；单段使用原90秒累计额度，parent180/两方向360秒不变。

原`.5`、`.6`、`.7`及失败/预算/独审阻断记录均保留在[阶段记录](../../claude-call-acceptance-20261109/docs/progress.md)。不能把当前PASS回写为旧失败通过。自然返工NOT_EXERCISED；typed resume、T09/native push及非macOS平台未纳入本轮，不提升这些能力的状态。

当前无待执行真实CLI验收；本次提交/推送已获用户授权，全局插件替换或其他产品能力仍需明确后续范围。历史总体材料见[恢复历史](resume-history-20261105-20261109.md)、[TASKS](../TASKS.json)及旧PKG/REG记录。
