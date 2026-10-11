# ACP local completion capacity

基线 b7ba13b。在真实 `persistACPCompletion` 写入口加入本地容量准入：最多 1024 个 immutable receipt、总大小 16 MiB；单文件原有 64 KiB 限制保持。每个 completion 目录用永久空 0600 `.quota.lock` 的 nofollow/nlink/owner/身份核验及非阻塞 flock 串行新增与重放，繁忙返回明确 busy。固定数量的目录读取避免扫描无界垃圾；未知名称、类型、权限、链接或 partial file 拒绝。

只有新回执接受容量检查；原有回执即使历史目录超出新限额，也仍可精确重放、原地 fsync。冲突回执不覆盖，满额不删除或驱逐任何恢复材料。此项限制真实完成回执的持久化，不是 Prompt 前资源预留，也不是 receipt GC：若本地写入被拒，原 controller 的 uncertain/不重复 Prompt 语义保持，容量错误不会授权新 Prompt。Store 全局配额与其他资源 GC 不在此项完成范围。

验证（Go 1.26.8，TMPDIR=/private/tmp）：

- RED `go test -json -count=1 -run '^TestACPCompletionCapacityPreservesExistingReceipts$' ./cmd/orchestrator`：count/bytes 两个容量边界旧实现均继续写入（1 top+2 sub FAIL）。raw `data/red.*`。
- GREEN `go test -race -json -count=1 -run '^TestACPCompletion' ./cmd/orchestrator`：6 top+12 sub PASS，0 fail/skip，stderr空。包括原有精确重放/冲突/unsafe文件/并发写守卫，以及新增容量后重放、busy锁/符号链接锁拒绝且零回执/外部内容未变。raw `data/race.*`。
- 容量负例使用任务独占的命名/权限合法占位文件（bytes 场景为稀疏文件），仅验证总文件/字节准入，不宣称物理磁盘满或真实模型洪泛。

未运行全量、真实模型/ACP、长测或物理磁盘故障；不启用清理，不操作用户runtime。临时fixture由Go测试清理，仅保留源码与raw。独立审查待主会话安排。
