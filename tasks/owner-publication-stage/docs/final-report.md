# F10 bootstrap publication stage 子任务

受限stage层已完成：生产源码固定 `56845145c92fab64cce934885c1163698740dc5d`（已授权fast-forward ROOT d557c2a），hostbootstrap定向race 6top/31named PASS0F/S，Windows amd64 leaf build exit0。只修改 `internal/hostbootstrap/*` 和本task；Store/Coordinator/journal/scratch/gates由ROOT及其他worker负责。完整F10独审、全模块build/vet和集成仍由ROOT验收，此报告不宣称所有F10完成。

## API与安全边界

- `Lease.Snapshot()` 在Read/Unchanged与root/held identity保护下返回原bytes副本，不重marshal。
- `PreparePublication(expected,replay)`：expected先限64KiB再复制；同heldlease/root核验，fresh拒绝任何旧/未知及同launchstage；replay只允许唯一expected派生stage，并核private UID/0600/regular/nlink1、FD/named inode/size/mtime和exactprefix。目录枚举最多4096entry，超限拒绝保留。
- 名称 `.publication-<shaLaunch>-<expectedSHA>`；旧无launch `.publication-<64hex>`、未知publication名和同launch旧 `.launch.json.replace-*`均preserve/failclosed。已归属其他launch的新稳定stage不阻塞/不改写本launch。
- `PublishPublication(expected,priorSHA)` 必须同lease已Prepare同frozenexpected；current只能prior/expected。existingexactprefix只补剩余，所有slice前均在Read后检查增长、尺寸、FD/named identity，修复旧Stat→Read增长panic。普通失败仅清本次新建且仍同private inode/root的stage；旧replay/replacement保留。
- expected已在final且stageabsent时只Sync/readback，保留inode；expected在final但stage仍在则拒绝保留。成功rename后held bootstrap身份只按自己创建/验证的stage更新。
- `VerifyPublication(expected)` 用于Finalize前及receipt前：namedexactexpected与held identity/root一致、file/dirSync/readback完成、stageabsent；它不要求再次Prepare，适配已published replay。新方法不创建进程或执行权限。
- ROOT已切新API，旧`PublishStable`草稿已删除；旧`Publish`仍供recover-host编译/既有语义。Windows提供unsupported stub。

## 验证

[7批原命令/源码/dirty状态/exit/实际计数/SHA](../data/evidence-index.json)，[original Wait与owned cleanup receipts](../data/owned-cleanup-receipts.json)。计数不含package pass，所有原FAIL保留。

- `stage-legacy-red`：1top4named FAIL，3个旧stage被旧实现忽略；业务RED对应新fresh拒绝GREEN。
- `stage-api-green`：3top16named PASS0F/S，含Snapshot/expected深冻结、不重marshal、alreadyexpected不换inode、expected+stage拒绝、错误prefix/oversize/link、Stat后真实增长不panic、mode/inode/final/prior漂移拒绝。
- `stage-native-01`：3top10named PASS0F/S，8个真实child在O_EXCL/partial write/fileSync/preRename由**原Process handle**发送SIGKILL，原Cmd.Wait确认SIGKILL，实际ConfirmGroupExitedContext证明group/tree exited。每次同stage≤1且始终同inode，最终exact replay发布该inode并Verify stageabsent。O_EXCL第二轮实际在原inode`stage_stated`杀掉，不伪称再次成功create。
- `stage-boundary-01`：1top9named PASS0F/S，归属/4097entry限界、>64KiB expected/零值lease、new普通error清理、replay/replacement保留。
- `stage-final-race`：clean5684514，6top31named PASS0F/S，exit0、stderr空；stdout SHA256 `4cc782bfc231bdffba218ca8815e48d9e4150db1a2f24ff6dcac03ba803a9346`。含同一native矩阵8次SIGKILL与cleanup guard的UNKNOWN/failed不删根分支。
- `stage-windows-leaf`：`GOOS=windows GOARCH=amd64 go build ./internal/hostbootstrap` exit0，stdout/stderr空，0个测试；只证明stub可编译。

两个native批各5个owner-private `/private/tmp/agent-bird-publication-stage-*`根都在pending0/原Wait/tree成立后removedtrue。native根不用t.TempDir，不在UNKNOWN时自动RemoveAll；失败保留精确root路径。其余无进程的文件fixture由Go测试管理清理。本任务无保留actor/binary，交付raw保留，不删其他WT/归属不明文件。

这是真实受控SIGKILL，不是自然内核故障或物理断电资格化。未伪造跨UID场景（UID校验存在，实际nlink/mode/soft+hardlink有oracle）。无真实provider/model/account、用户安装替换、push/release；未跑全Store/全仓，未验本轮Linux native/Windows native、Formal60或资源资格。scratch一次计费/释放、Store COMMIT、pending gates、公开owner重绑链由ROOT独立负责，不借用其证据冒称本stage覆盖。
