# 完成后 Owner／Host 自然退出验收

范围：真实公开 deadBoth Resume 保持同 attempt、新 segment、预算和恢复账本后，经 collect/accept 完成，原 Owner 与 FirstHost 自然退出；修复完成终止的 stale capability replay/conflict，以及 Linux FD4 在成功 Owner 退出后返回 ECONNRESET。没有扩大 Provider、权限、重试、平台或安装范围。

生产变更和依据见 progress.md。原 Wait observer checkpoint9f71e96 是私有 per-call seam，生产传 nil；实际公开 CLI 测试持 FirstHost 原 Cmd，observer 只回传唯一 Owner Cmd.Wait 的 ProcessState/error，不伪造死亡或重建 Wait。

## 最终证据

| 范围 | 结果 | 原始证据 |
|---|---|---|
| macOS Host race | 53 顶层／165 具名 PASS，0 FAIL/SKIP，75.775s | data/root-retirement-reset-final-race.jsonl 的 Host package；同文件 CLI 首次失败不可计通过 |
| macOS CLI 最终 race | 9 顶层／13 具名 PASS，0 FAIL/SKIP，52.720s | data/root-cli-reset-final-race.jsonl；包含旧 signed/replacement/consecutive/restart、新 original Wait 负控和完整公开 Resume |
| Linux Host | 28 具名 PASS，0 FAIL/SKIP | data/linux/host-reset-green.log、对应 receipt |
| Linux CLI 最终 | 7 具名 PASS，0 FAIL/SKIP | data/linux/public-cli-final.log、对应 receipt；完整公开 Resume 和原 Wait 负控 |
| 静态与构建 | host/CLI vet0，whole module build0，diff check0 | data/root-reset-vet.log、root-reset-build.log；明确原命令终态记录 |
| 独立审查 | 完成终止、原 Wait fixture、Linux reset/portable fixture 全部 PASS | 主会话独立审查记录；新准备窗口有采样诊断与只读复核 |

Linux 使用固定本地镜像、禁网、只读 root、非 root、cap-drop ALL、私有 tmpfs、只读 binary mount。最终两个精确 CID 均 exit0、非 OOM 并已 rm/inspect absent；对应临时二进制经 SHA 核对后删除。没有 Linux race、真实 Provider/model/tools、Intel/Windows 原生验收声明。

完整 source/hash/count 见 data/final-receipt.json。Mac Host 和最终 CLI 具名合计178，Linux35；不跨模式重复相加为独立场景数。

## 保留的失败和限制

- root-cleanup-compile-failure 是测试编译错误，不是行为 RED；root-cleanup-behavior-red 才是 stale registration 的有效 RED。
- root-public-green 文件名不代表通过：产品自然退出后旧 fixture 仍要求进程存活再 Signal，实际失败已保留；修为自然原 Wait 后通过。
- Linux host 初次因 /bin/sleep 为非规范 symlink 拒绝；只改测试可执行夹具。Linux public 初次真实 Owner Wait0／FirstHost Wait2、FD4 reset，随后精确原 Wait gate 修复通过。
- 两次 Mac race public 在5s Coordinator 准备窗失败，Owner尚未创建，原 Coordinator Wait为SIGTERM，不能冒充 graceful0。文件 root-retirement-reset-final-race（仅其CLI部分）及 root-public-reset-race-recheck 保留。相同 binary 的独立原进程采样定位 SQLite schema migration，socket出现耗时4.445s。测试准备窗调整至15s后最终真正ready耗时5.165s，随后完整流程及所有原Wait通过；生产迁移和70s总context/13s退休契约未变。
- 所有失败 fixture 保留，不删除来源不明进程/文件。额外诊断 fresh state 已在原 Popen Wait0 后移除，sample/符号化和JSON回执保留。没有强行将失败日志改为通过。
- 用户要求本小功能完成后停止：schema28 bound abandonment仍tests-only8b9e214；F12 crash草稿07ccd23未编译/测试/审查、未合入。完整剩余提示词在根任务 docs/continuation-prompts.md。

## 集成终验

85c2203（公开原 Wait fixture）与 dd5f977（修复）已本地合入 feat/capability-closure-20261006；12 项 source SHA 与验收快照逐项一致。集成后定向回归 Host3顶层/20具名、CLI3顶层/7具名全部PASS、0FAIL/SKIP，整库build和host/CLI vet均exit0。原日志、统计、hash见 data/integrated-{regression.jsonl,receipt.json}。未推送/发布；当前无运行测试或本任务容器。
