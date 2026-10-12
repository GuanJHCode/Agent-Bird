# Verification

日志保存在 `tasks/git-cleanup-replay/data/`，每次命令使用独立 stdout、stderr 和 exit 文件。

全包 `internal/gitops` 基线包含缺失 native fixture 和既有 process timing 失败；本任务使用 focused cleanup 与 durable-journal 测试作为验收。
