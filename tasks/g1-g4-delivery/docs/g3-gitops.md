# G3 Git/worktree boundary

本模块位于 `tools/orchestrator/internal/gitops`，供 G1 Store 通过 `Journal`、`CandidateJournal` 接入。它只接受不可变 commit OID 和已审查的 `ReviewBinding`，不修改 coordinator 数据库。

## 接口与不变量

`Materialize` 在独立 worktree 中验证 base、candidate、依赖祖先关系，记录候选 commit/tree、ordered inputs、common-dir、文件身份和目录的 device/inode/uid 身份。`ComposeCandidate` 则在独立 worktree 以明确顺序组合依赖：可快进时使用受保护的 ff-only，独立分支使用受保护的普通 merge 并生成组合 commit；冲突保留现场并由上层按意图账本处理。最终消费者整合仍只允许 ff-only。

`Integrate` 要求 review 同时绑定 canonical worktree、目录内核身份、symbolic ref、base/candidate OID、ordered inputs 和 review/plan revision。它在 common-dir 的 OS `flock` 内重新检查 ref、HEAD、index、tracked/ignored 碰撞和依赖祖先关系，然后只执行受保护的 fast-forward merge。后置必须同时满足 symbolic ref、HEAD、candidate tree、index tree 和 clean status；任何漂移或账本记录失败都返回 `integration_uncertain`，不会自动重试。

私有 ref 只允许 `refs/orchestrator/g3/` 命名空间，创建/更新和删除都携带 expected OID 做 CAS；不会使用任意 `update-ref` 修改用户分支。

## 清理边界

`Cleanup` 先持有 worktree 根目录、父目录、Git common-dir 锁和该 worktree 专属 admin 目录句柄，复核目录和每个 tracked/untracked/ignored 条目的 device/inode/mode/size/mtime 身份。未知文件、symlink、身份变化、活跃 PID 或账本失败都保留现场并返回 `cleanup_pending`/`unknown`。已分类的根目录先通过父目录 FD 移入同父目录的私有 quarantine；其中每个清单项再以 `openat` 打开父组件、用 `renameat` 原子移入新的 0700 删除隔离目录、通过 FD 复核身份，最后才 `unlinkat`。晚到替换会被移入隔离目录并保留，晚到新增会使已知目录无法删除并保留。

Git CLI 没有 dirfd 版本的 `worktree remove`。实现因此不会把可被重建的原 worktree 路径传给 Git：它核对 `.git` 与 `<common-dir>/worktrees/<id>/gitdir` 的双向绑定，只对这一个 admin 目录做同样的 FD 锚定清单回收，不调用全仓 prune/GC，也不触碰 refs 或 objects。若原名称被重建，它不会被删除，结果保持 `cleanup_pending`。若内核句柄核验、admin 清理或 quarantine 回收不能完成，也不宣称完全清理。

## G1 接入

G1 已通过来源 Host 的 `gitops-worker` 接入 `compose`、`materialize`、
`integrate` 和 `cleanup`。控制面只派发带不可变身份的 worker grant；来源
Host 将 snake_case 请求固化为 0600 文件，并在执行 Git 前把意图写入 0700
目录下的 durable journal。worker 结果沿正常 Host event、collect 和 ACK 链
返回，协调器进程不直接执行 Git。

worker journal 实现 `PersistIntegrationIntent`/`RecordIntegration`、
`PersistCleanupIntent`/`RecordCleanup` 和候选冻结的两个 `CandidateJournal`
回调。所有 receipt 的目录身份和 OID 来自本模块返回值，调用方不从路径或
可变 JSON 自报。

验证命令（使用仓库固定 Go runtime）：

```text
cd tools/orchestrator && <fixed-go> test ./internal/gitops ./internal/gitopsworker
```

测试使用真实临时 Git 仓库覆盖候选材料化、ordered dependency composition、review binding、OS lock 下的 fast-forward、tracked/ignored 碰撞、目标漂移、CAS private ref、外部 sentinel、根目录替换、晚到同名替换的隔离保留和 admin 清理。未覆盖远程 push、Windows 文件占用和恶意同 UID 进程竞争。
