# 2026-10-12 剩余能力交付

已合入的实现：Linux collect-only followup；诊断自动 GC（默认关闭、窄 run 授权、7天成功消费 TTL）；ACP 完成回执 quota + Prompt 前 reservation；Darwin filter authenticated carrier、独立 companion metadata quota/journal、target transition 与显式 filter_integrate Host/DAG/CLI 接线。

ACP 容量与诊断 GC 已通过定向验证和独立审查。B2/C/Host evidence/public join/D pre-start guard 均已分批审查通过。最终 filtered merge 末阶段原生验证 PASS：1 top、165.91s、shell exit0，实际 target HEAD/bytes 与候选相符、原 Wait/tree、两份预约由同一 Exited 释放并 accept。前五阶段已在先前真实受控运行接受；证据复用这些 immutable managed artifacts，由全新末阶段运行资源完成续跑，不声明新的单次六阶段运行。D callback 漂移回归另有 1 top + 1 sub PASS。详见[验证记录](../../filter-public-integration/docs/validation.md)。

验证只覆盖受控本地进程与明确协议。真实模型/账号、原 TUI/Hook 五窗口、Intel/Windows、物理断电/洪峰和 Formal60 不在本次通过范围；Formal60 历史 FAILED 保留。Codex Worker 执行域仍受嵌套 Seatbelt 限制，未放宽原有写入/网络防护。

用户已选择显式确认已解决后按保留期回收，并保留恢复索引。schema39 已落地 managed report 附件闭环：逐项失败确认、精确消费/终态资格、成功30天与失败7天、保存计划与崩溃恢复、原ACK重放和默认关闭自动GC；见[验证](../../report-retention/docs/validation.md)。Host 附件/进度和 candidate worktree/private ref 的回收仍在后续范围，不能按 created_at 直接删除 immutable/recovery 资源。

本候选只作本地集成。未替换用户安装、注册 Hook、推送或发布。原根用户补丁继续保留。

本阶段候选包与来源已通过[独立核验](../../closure-package-provenance/docs/all-capabilities-20261012.md)，后续清理实现需重新评估包。

普通非 filter AutoDirectory validate/review 的成功临时工作树清理已落地，独立状态展示，业务产物不变。补齐 Git partial-quarantine 重放保护、持久 journal 并发边界，以及真实 Host 回执在保留期检查中的严格解码兼容。最终短 native 1 top、Store race 4 top+8 sub、Git/journal race 7 top 均通过；见[本批记录](../../candidate-temporary-cleanup/docs/validation.md)。共享候选/private-ref TTL 和 Host 附件回收继续推进，当前历史包尚未包含本批与 schema39。
