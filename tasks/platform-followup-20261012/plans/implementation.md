# macOS platform followup implementation

基线：7861d2687b2df9c9be43056197ba23d60d2daac5；schema41。
目标与范围以本轮用户请求及 non-macos continuation 为准。主会话独占 integration，两个实现者独占外层 task tmp 下 worktree；不递归。共享 contract/event/schema/CLI 主会话负责。

- [ ] Codex：固定0.154.0执行接口核验；选择独立受约束工具执行域，保留文件/网络/状态防护。补读写/续接的必要回归与原生短链，只有全部准入契约成立才开放公共入口。
- [ ] Darwin Git（darwin_git）：复用现有 Broker/carrier/转换/冻结；补扩展执行底层，保留 LFS/hooks/signing 上层门禁。
- [ ] 监督（主会话）：核对 original-owner/原Wait现状，明确新任务可恢复条件与历史UNKNOWN边界；补版本化可查询契约及相关缺口。
- [ ] AGY/资源（agy_resource）：定位 source_host_start_failed，修复可证实根因；Formal60不从RSS波动推断泄漏，不重跑长测。
- [ ] 平台接口（主会话）：版本化能力查询，区分底层实现/公共入口/验收，不让底层存在隐式开放动作；固定事件及哈希规则。
- [ ] 集成与审查：按功能定向 JSON 回归、相关 race、独立审查，逐功能提交并推送 feat/macos-followup-20261012。最终handoff列有效证据、产品codeSHA、schema起止、接口与真实阻塞。

验证策略：TDD仅补业务边界回归；复用未受影响旧证据。不默认全仓测试、长资源测试、真实Provider或TUI。原生合成、交叉编译、真实Provider/TUI分别标记。
审查重点：网络/状态权限退化；原Wait丢失被误判恢复；公共能力误报；扩展执行越界；启动错误造成槽/进程泄漏。

进度：仓库/远端/HEAD/干净状态已核对，工作分支已创建。原有嵌套Seatbelt N/P证据复用，不重复诊断；单独 externalSandbox 字段不足以保住工具网络隔离。
