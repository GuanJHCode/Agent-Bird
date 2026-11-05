# 当前实施合同

采用 plans/handoff/01-implementation-plan.md 与 04-acceptance-matrix.md，依赖以 TASKS.json 为准。用户已授权功能逐项 push；仅 push feat/cross-cli-hardening-20261105，不 merge 正式分支、不发布 release、不替换全局安装。

- T00：主会话基线、独审和签收后提交。
- 第一批 T01/T02 隔离 worktree 并行；主会话准备 T10 内容比对。运行时最多两个子 agent，不递归委派。实现者不得先行提交。
- 每功能冻结 diff 与证据；另一 agent 只读独审；问题修复后复核；主会话检查实际 diff/测试，再提交并 push。
- 后续按已冻结状态/预算/反馈/身份契约推进；共享 schema/Store/CLI 变更先 ADR，集成树唯一写入者。
- Go 测试期间其 worktree 冻结；各 worker 独立缓存/运行目录。真实模型试验串行且先记录预算，不扩大权限。
- 最终同一包验收；fixture、OS-native-local、installed-package、live 分开。缺前提保留 BLOCKED，不包装为 PASS。

ADR 索引：ADR-001-contract-boundaries.md。每项 review 和主签收保存在 reviews，状态和证据随里程碑更新。
