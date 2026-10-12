# macOS followup progress

起点7861d2687b2df9c9be43056197ba23d60d2daac5，远端Agent-Bird；integration原为干净linked worktree，工作分支feat/macos-followup-20261012。

2026-10-12：版本化平台只读查询 2e0f79b 已push；3顶层/7命名PASS，0FAIL/0SKIP。独审进行中。schema41未变。

AGY调查：定向原fixture复现实际为旧故障注入锚点失配，不是已证明的生产启动失败；私有worker正在修复两处CLI测试。Formal60原失败不重跑，后续1200s诊断无产品泄漏归因，不做无依据内存配置改动。

Darwin filtered现有Broker/carrier/原Wait、转换/冻结/ff-only整合已存在。LFS/hooks/signing所需资源许可与pin、外部交互及副作用对账尚无完整合同，保持门禁。Codex固定upstream 6b9826的exec-server独立执行域接口正在核验。已有嵌套Seatbelt EPERM证据复用；不尝试删权限保护。

所有者：主会话integration（共享契约/CLI/docs）；agy_resource独占外层task tmp/agy-resource；darwin_git独占tmp/darwin-git，当前只读审查与Codex调查。不递归。
