# G3 Git/worktree boundary

本模块位于 `tools/orchestrator/internal/gitops`，供 G1 Store 通过 `Journal`、`CandidateJournal` 接入。它只接受不可变 commit OID 和已审查的 `ReviewBinding`，不修改 coordinator 数据库。

## 接口与不变量

`Materialize` 在独立 worktree 中验证 base、candidate、依赖祖先关系，记录候选 commit/tree、ordered inputs、common-dir、文件身份和目录的 device/inode/uid 身份。`ComposeCandidate` 则在独立 worktree 以明确顺序执行 `merge --ff-only --no-autostash --no-overwrite-ignore`，并返回冻结的 candidate receipt；失败现场由上层按意图账本保留。

`Integrate` 要求 review 同时绑定 canonical worktree、目录内核身份、symbolic ref、base/candidate OID、ordered inputs 和 review/plan revision。它在 common-dir 的 OS `flock` 内重新检查 ref、HEAD、index、tracked/ignored 碰撞和依赖祖先关系，然后只执行受保护的 fast-forward merge。后置必须同时满足 symbolic ref、HEAD、candidate tree、index tree 和 clean status；任何漂移或账本记录失败都返回 `integration_uncertain`，不会自动重试。

私有 ref 只允许 `refs/orchestrator/g3/` 命名空间，创建/更新和删除都携带 expected OID 做 CAS；不会使用任意 `update-ref` 修改用户分支。

## 清理边界

`Cleanup` 先持有 worktree 根目录句柄，复核目录和每个 tracked/untracked/ignored 条目的 device/inode/mode/size/mtime 身份。未知文件、symlink、身份变化、活跃 PID 或账本失败都保留现场并返回 `cleanup_pending`/`unknown`。已分类的目录先移动到同父目录的私有 quarantine，重新用持有的根句柄核验，再让 `git worktree remove` 清理 Git 管理记录，最后只删除核验过的 quarantine 树；未知对象永不传给删除操作。

Git CLI 本身没有 dirfd 版本的 `worktree remove`；因此实现依靠持有根句柄、quarantine、移动后再次身份核验和失败保留来缩小 pathname race。若内核句柄核验、Git 管理清理或 quarantine 复核不能完成，结果保持未知，不宣称完全清理。

## G1 接入

G1 Store 应在 `PersistIntegrationIntent`/`RecordIntegration`、`PersistCleanupIntent`/`RecordCleanup` 外增加候选冻结的两个回调，或用适配器实现 `CandidateJournal`。回调必须先持久化 intent，再把 receipt、验证摘要和依赖顺序写入同一 plan revision。所有 receipt 的目录身份和 OID 必须由本模块返回值绑定，不能由调用方从路径或可变 JSON 自报。

验证命令（使用仓库固定 Go runtime）：

```text
GOCACHE="$PWD/tmp/gocache" GO111MODULE=off <fixed-go> test ./tools/orchestrator/internal/gitops
```

测试使用真实临时 Git 仓库覆盖候选材料化、ordered dependency composition、review binding、OS lock 下的 fast-forward、tracked/ignored 碰撞、目标漂移、CAS private ref、外部 sentinel、根目录替换和 quarantine 清理。未覆盖真实项目分支整合、远程 push、Windows 文件占用和恶意同 UID 进程竞争。
