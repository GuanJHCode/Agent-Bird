# Retained lifecycle 独立复核

审查范围：`66bb9f0..5b96919`；首审为 `ba4d19e`，最终增量为直接父守卫、两项审查修复及旧 segment CAS 回归断言。源码 worktree、索引与 HEAD 均未修改；验证使用固定 OID 的 `git archive` 独立副本。

## 结论

**Ready to merge: Yes。可合并并进入合同限定的原生测试。**

Critical：无。Important：无剩余项。Minor：无。

这一结论只覆盖 Go 合成生命周期夹具。原生 Codex 身份、退出、恢复和明确继续仍需独立实验；在线真正 idle 自动回传要求仍然保留，完整 G0 未因此放行。

## 实现核对

- start/resume 使用稳定 inode 的非阻塞独占 flock；owner、任务 revision 与 expected segment 均在锁内校验，完成态也不绕过 CAS。
- 不可覆盖的连续 step 产物决定已完成进展。状态缓存落后可对账，全步骤已存在的重复继续不新增 segment、不重做产物；未知或损坏权威文件拒绝覆盖。
- inspect 只读，前后原子状态采样包围产物扫描，始终输出 `liveness_checked:false`。恢复中的 stage 清理限定在已知、验证过类型／权限／inode／链接数／大小的路径。
- 直接父监测只取消执行，不并发写业务记录；业务写入与停止 checkpoint 仍在原执行路径完成。初始 PPID <= 1 拒绝，父变化在等待、输出阻塞和步骤提交前被检查。
- 允许父退出与一次原子产物提交竞争，并保留已提交产物；文档未把直接父守卫推广为任意进程树、Codex 身份或 TUI 附着证明。
- 时间字段要求规范 UTC RFC3339Nano round-trip，schema、IDs、hash、枚举和数值均在输出前验证；未发现历史任意字段文本回显回归。
- 旧 start/read/wait/ACK 的来源文件与行为分支保留；新增有界 stderr 仅作用于 retained 子命令。

## 已修复的首审问题

1. `ba4d19e` 的 `retained.go:148–162,175` 在正常发布移除 staging 硬链接后可能误拒健康产物。最终版本重新验证原路径的相同 inode 和当前单链接状态，保留 foreign hardlink 拒绝。
2. `ba4d19e` 的取消路径返回 main 后，最终同步 stderr 写可在合流满管道中阻塞。最终版本仅为 retained 最终错误输出增加 250 ms 上限，随后保持原退出码。

两项均由 reviewer 在原版本独立复现，再对最终固定版本复测转绿。第一项是观察暂态误拒，未观察到数据损坏或重放；第二项是业务写已停止但进程退出失去界限。

## 新鲜验证

环境：现有 Go 1.26.8 darwin/arm64 工具链，`GOTOOLCHAIN=local`，独立任务 GOCACHE/GOMODCACHE/TMPDIR/GOTMPDIR；无全局配置变更。

| 验证 | 结果 |
| --- | --- |
| `go test -count=1 -timeout=90s -run 'TestRetained\|TestReview' ./...` | exit 0，`ok g0-return-lab 17.508s`；14 个 retained 顶层测试及 reviewer 原始文件竞争复现，含父退出三个屏障和完成后旧 CAS |
| `go build -o ABS_REVIEW_TMP/g0-return-lab-5b96919 .` | exit 0 |
| reviewer 原始合流满管道 Python 复现，改用最终固定版本二进制 | 65,536 字节满管道，SIGTERM 后一秒内退出码 2；记录 `interrupted`、`completed_steps=0` |
| `git diff --check 66bb9f0..5b96919` | exit 0，无输出 |
| 文件范围检查 | 仅 `tools/g0-return-lab/` 与授权的 `host-validation.md` |

实现者报告的完整 normal/race/vet/build 已检查文档记录；此处不冒充 reviewer 独立重跑了完整 baseline/race/vet。主 Agent 另行执行主树整合 full race/vet。

候选原生测试二进制 SHA-256 已独立读取验证：

```text
feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285
```

固定提交生产源 SHA-256 与实现者记录一致：

```text
main.go      fd3a7699abdd7b963ea6f70bd5823f9b350980561dc9925245a4469f881b7544
retained.go  144c199e3dc863b30517500e57428f689c09a8bbbb13d19c58ee11ff5776650d
ack.go       20767515b37be6ab23c3bb3d5d62f079b99f81ce26d68f671a54c11be1dbf2da
```

## 清理

仅本 reviewer 产生的临时源码副本、测试产物、二进制、缓存及复现脚本在保存本报告后删除；保留本目录中的两份审查记录。所有由 reviewer 启动的复现进程均已退出或被其自身 harness 杀死并回收。实现者 worktree、原生实验材料及其他任务文件未删除。
