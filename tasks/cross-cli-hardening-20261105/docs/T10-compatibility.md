# T10 兼容目录合流

基线 29691e9f，对照 4b2b4916。两个历史无共同祖先，不合并分支，只恢复已冻结的 Claude provider 目录对象。旧树405版本（1.0.61–2.1.317），当前46版本（2.1.267–2.1.316）是旧集合的严格子集，缺359项，无新增；共同版本的发布时间相同。git历史未找到撤销较宽只读窗口的ADR；因此选择恢复旧冻结目录，不以今日网络latest替换。

Claude对象与4b2b4916逐字段一致；全局frozen_at取该Claude快照日期。AGY/Grok/Codex三个对象与基线及旧树逐字段一致，保留各自在2026-11-02的快照来源。CodexNativePins的21个摘要不变，admit/profile/Host/handle生产代码无改动。

已有旧cohort逻辑继续生效：低于2.1.267仅typed reviewer/read-only；不允许无profile、workspace、action、implementer。旧版本flag拼写适配保持；新旧cohort都必须声明正向--tools，缺失失败关闭。Claude coding仍仅exact 2.1.267/2.1.316+对应SHA；Codex coding仍仅0.166.0 exact native pin。只读目录不是逐版本功能、认证或模型验收，旧CLI若不具备当前能力仍拒绝。

回归先在窄目录复现失败，再恢复目录验证405集合/历史flag/缺--tools拒绝/旧cohort越权/全目录拒绝未知写摘要，以及现有adapter和Host相关测试。测试和独审结果由集成主会话补充；不宣称已通过所有验证。本任务未改变已有handle路由、owner或provider lock，旧任务继续使用原runtime；兼容归并无数据库迁移。

实际验证：adapter 103测试项PASS/0fail/1skip（opt-in probe观察），Host门禁相关7项PASS/0fail/0skip。最终源码摘要和环境见T10-green-r4.json/T10-host.json；目录比较见T10-catalog-comparison.json。原始日志保留外部任务tmp/t10-evidence，以公开摘要SHA核对。独立审查及主会话签收PASS，见reviews/T10-review.md。
