# F10 publication stage

基线1215fd7；本WT唯一writer负责internal/hostbootstrap与本task，ROOT独占Store/journal/Coordinator，另一worker负责gates。不派生、不修改cmd/Store/Coordinator。按冻结owner-bootstrap-publication implementation计划执行，ROOT新接口另明确launch+expected双SHA稳定名。

1. legacy/unknown stage拒绝业务RED，随后实现Snapshot/PreparePublication/PublishPublication/VerifyPublication及windowsstub，尽早固定生产提交。
2. 受控child SIGKILL在created/partial-write/fileSync/preRename；per-call private checkpoint，无global/env生产口。原Cmd.Wait/tree确证后才清理，UNKNOWN保留根。
3. 边界聚合与一次focused race；ROOT独审/集成及最终wholebuild/vet，原FAIL/raw/count/source/Wait保留。

无真实provider/model/account、安装替换/push/release；stage只发布metadata，不创建执行权限；scratch计费由ROOT Store负责，本task不测试该账本。


生产第一里程碑：Snapshot/PreparePublication/PublishPublication/VerifyPublication与Windows unsupported stub已实现。Publish要求同lease成功Prepare和同expected bytes；新双SHA稳定stage、bounded4096目录扫描、fresh拒绝旧/未知stage、replay唯一stage与exactprefix补余字节；所有切片前检查read后尺寸和FD/named身份。expected已在final且stageabsent只Sync/readback，无新inode。Verify检查held final/root、file+dirSync与stageabsent。旧PublishStable暂为fresh-only过渡wrapper，不允许无journal采用stage，待ROOT切换后删除。

stage-legacy-red 1top/4named FAIL（3子例+父例），旧random/64hex/unknown被旧实现忽略；原raw保留。stage-core-green 1top4named PASS，stage-api-green 3top16named PASS0F/S；后者含Stat后增长拒绝且不panic、深冻结Snapshot/expected bytes、replay stage保留、Mode/UID-nlink/inode/root final检查。UID差异未用特权伪造，实际mode/nlink/soft+hard link分支有oracle。真实SIGKILL尚未验，不称完整stage完成。


第二里程碑：ROOT已接入新API，经授权fast-forward d557c2a，仅删除hostbootstrap旧PublishStable（旧Publish供recover-host保留），测试fresh guard改直接Prepare。增加expected尺寸先限界后复制和零值/未held lease拒绝。

stage-native-01 3top10named PASS0F/S：8个真实child由原Process handle SIGKILL，每个原Cmd.Wait明确SIGKILL且ConfirmGroupExitedContext成功；同一stage inode至多1，created首次O_EXCL后第二次在原inodestage_stated杀掉（不伪称再次create），partial/fileSync/preRename各重复杀2次，最终exactprefix补余、rename同inode、Verify stageabsent。4个native根+无actor的cleanup guard根均pending0/removedtrue；UNKNOWN/failed guard返回不清理。原badStage prefix/inode/mode/links/growth保留，普通异常只清本次新建同inode。

stage-boundary-01 1top9named PASS0F/S，含其他launch稳定stage不干扰/不改写、同launch另一expected拒绝、4097entry failclosed、超64KiB expected/零值lease拒绝、new/error清理 vs replay/replacement保留。ROOT Store scratch/nativepublic链独立证据不由此冒称。下一最后focused race与Windows leaf编译。


最终clean5684514 stage-final-race：6top31named PASS0F/S、exit0、stderr空，SHA4cc782bfc231bdffba218ca8815e48d9e4150db1a2f24ff6dcac03ba803a9346。8个真实child原SIGKILL Wait/tree与5个owned根removedtrue，未出现UNKNOWN清理。Windows amd64 hostbootstrap leaf go build exit0、stdout/stderr空，不宣称Windows native/全模块编译。原全部raw与RED保留；后续仅docs/evidence，无新测试。ROOT负责完整源码独审/集成及全模块build/vet，scratch/quota/nativepublic证据不计入本子任务。
