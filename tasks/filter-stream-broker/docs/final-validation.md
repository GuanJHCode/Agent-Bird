# Bounded filter stream executor 验收与交接

生产/最终测试固定 `1ec0cb5`，前置计划 `515dd96` 经独立计划复审 PASS。测试源先固定 `8410eec` 执行真实业务 RED；实现 `a893aab` 后 native GREEN；held directory 与失败收尾收紧及最终回归固定 `1ec0cb5`。此次只有 internal/gitfilter、严格 native parser 抽取/Host委托和本任务证据变化，不改变公共 filter gate。

## 行为与源码

- `internal/gitfilter/executor.go`：内部 Request/Result，无 wire grant、CleanupReceipt、Store权限或冷恢复。
- `executor_darwin.go:166` Execute：入口深复制 Args并捕获 operation/scope/tool；Scope仅canonical opaque观察绑定。输入/output由caller独占且拒绝同dev/inode，output为空single-link0600、O_RDWR非APPEND。从0 WriteAt、最终FD size==written<=16MiB，FD回读摘要核验，不从File.Name打开输入/output路径。
- 同时一个original process；复用 StartDuplex和CloseWrite，两条有界流通过32KiB buffer读写。单输入/output16MiB，sealed native tool32MiB；必须有deadline，实际运行deadline最多60秒，无ReadAll或stdin整体复制。
- 验证工具named/held inode/UID/nlink/0500/size/hash/native entry，工具parent和empty working directory canonical/currentUID/0500；保持原directory FD并前后复核held+named identity。emptydir前后必须无条目，工具parent不能等同emptydir。caller负责预先seal、资源预约及祖先隔离，本层不声称关闭恶意同UID外部写入TOCTOU。
- ExactEnv固定HOME/TMPDIR/XDG_CACHE_HOME为只读emptydir、最小系统PATH。无caller env、scratch或filename expansion。Seatbelt deny文件内容读/写/link、network、fork、process-exec；仅literal sealed tool可exec，允许列明OS runtime、metadata、字面根目录loader读取、随机设备及/dev/null。允许同tool selfexec，不声称禁止它；仍受pin/前后identity/hash与deadline限制。
- 失败先Close stream，对任意non-nil original Handle（包括nil stream）使用独立fresh bounded cleanup context Stop→Wait→tree，然后join本次全部I/O goroutine。原Wait/tree不能确证则UNKNOWN。Accepted必须Wait nil、ExitCode0、tree nil且所有I/O和物理/hash复核通过；非0exit是已知终态但拒绝。nil Handle未启动只是观察，不是重试、退款、unlink权限。
- `internal/process/native_executable.go:12` 原样抽取既有严格Mach-O/ELF格式/entry parser；`host/validation_tool.go:22`仅委托，F01 seal/dispatcher guard保持。
- `executor_other.go`明确unsupported；没有非Darwin未隔离fallback。

## 实际证据

Pinned Go1.26.8，TMPDIR=/private/tmp，Darwin。runner `scripts/run-focused.py`记录每批command/cwd/HEAD/dirty/toolchain；每次执行前测试commit或hash已固定。`data/final-summary.json`给出实际逐具名结果、完整raw SHA、当前源码SHA和精确fixture清理状态。所有stderr为空。

| batch | exit | top / named | 实际结果 |
| --- | --- | --- | --- |
| stream-red | 1 | 2 / 11 | 7 FAIL、4 PASS；safe scaffold拒绝真实转换/边界/非0/cancel，保留原FAIL |
| stream-green-01 | 0 | 2 / 11 | 11 PASS、0 FAIL/SKIP |
| stream-delta-green-01 | 0 | 1 / 5 | 5 PASS、0 FAIL/SKIP；执行前test-source-next.json固定hash |
| stream-final-race | 0 | 4 / 25 | 25 PASS、0 FAIL/SKIP；最终clean1ec0cb5 |

最终race命令：

```
go -C tools/orchestrator test -json -race -count=1 ./internal/gitfilter ./internal/host -run '^(TestStreamExecutor.*|TestValidationSealedToolPinAndBoundary)$'
```

最终stdout SHA256：`0652fbd0ac0a1a70d90808ae88ad879827585ca4d80857467b0630c996507f8b`。新gitfilter为3 top/17 named；原F01 sealed-tool代表性回归1 top/8 named。未跑全Host/Store或全仓测试；ROOT接管whole build/vet及固定源码独立审查，不在本报告冒称其已经通过。

Native oracle包含真实xor clean/smudge恢复canonical bytes、exact16MiB、input/output超限、cancel原Wait/tree、exit7已知terminal、distinctFD同inode拒绝、O_APPEND/只读output拒绝、原pin/可写tool拒绝、scope格式与深冻结、真实Start后non-nilHandle/nilstream失败的原收尾、输入与目录漂移。kernel oracle有未sandbox的7项positive control（external read/write、emptydir chmod/write、self chmod、fork、TCP连接）以及独立in-place exec到/bin/echo positive marker；sandbox逐项EPERM/EACCES拒绝，in-place exec返回deny marker。emptydir未变化、sentinel原字节保留。没有以只读权限自身拒绝或executing-text行为代替sandbox positive control。

## 清理与限制

所有build、control、实际Execute均持原Handle并记录原Wait/tree；非0/受控cancel/受控Start后错误同样有终态。测试不用t.TempDir自动删actor根；task-private os.MkdirTemp根由guard在已知原终态时清理，UNKNOWN保留。8个精确root现均absent，详见final-summary；没有PID猜测signal、复制安装或外部账户操作。本任务保留完整raw、source hash和报告，tmp无遗留fixture。

真实内核UNKNOWN、真实系统资源耗尽、same-tool selfexec loop、其他平台runtime、资源/RSS资格未验；没有强造伪PID/冷JSON退出proof。新层没有创建/删除/退款持久文件，没有Store grant认证、sealed工具复制/持久journal/冷恢复；预建工具/目录/input/output和资源归caller。它未提供Git执行、LFS/cache/tmp、hooks/signing隔离，也未接producer→validate→review、target、compose或indexed view传播。完整F04和公共filter支持均仍未完成，现候选filter拒绝保留。
