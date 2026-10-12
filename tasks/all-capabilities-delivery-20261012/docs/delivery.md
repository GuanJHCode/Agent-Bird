# 2026-10-12 剩余能力交付

已合入的实现：Linux collect-only followup；诊断自动 GC（默认关闭、窄 run 授权、7天成功消费 TTL）；ACP 完成回执 quota + Prompt 前 reservation；Darwin filter authenticated carrier、独立 companion metadata quota/journal、target transition 与显式 filter_integrate Host/DAG/CLI 接线。

ACP 容量与诊断 GC 已通过定向验证和独立审查。B2/C/Host evidence/public join/D pre-start guard 均已分批审查通过。最终 filtered merge 末阶段原生验证 PASS：1 top、165.91s、shell exit0，实际 target HEAD/bytes 与候选相符、原 Wait/tree、两份预约由同一 Exited 释放并 accept。前五阶段已在先前真实受控运行接受；证据复用这些 immutable managed artifacts，由全新末阶段运行资源完成续跑，不声明新的单次六阶段运行。D callback 漂移回归另有 1 top + 1 sub PASS。详见[验证记录](../../filter-public-integration/docs/validation.md)。

验证只覆盖受控本地进程与明确协议。真实模型/账号、原 TUI/Hook 五窗口、Intel/Windows、物理断电/洪峰和 Formal60 不在本次通过范围；Formal60 历史 FAILED 保留。Codex Worker 执行域仍受嵌套 Seatbelt 限制，未放宽原有写入/网络防护。

其他 artifact/report、失败现场、worktree/ref pin 的自动回收尚缺实体清单、保留时钟、持久 resolved/abandon 与恢复 pin 释放转换。已向用户询问清理规则，不按 created_at 直接删除 immutable/recovery 资源。

本候选只作本地集成。未替换用户安装、注册 Hook、推送或发布。原根用户补丁继续保留。
