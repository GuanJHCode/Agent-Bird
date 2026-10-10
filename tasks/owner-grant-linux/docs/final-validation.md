# Linux owner-bind publication 定向验收

生产/测试源码固定 `e50bf47e9e3099b964d81f9c5af5a85d44f0995d`。新验证WT只增加本任务runner/证据/报告；runner先固定 `e7ff173` 再执行，tools/orchestrator完整Git tree与e50一致，逐文件SHA及Go版本记录在data/source.json。未改生产、测试或旧WT。

实际一次Linux运行 **2 top / 4 named PASS，0 FAIL/SKIP**：

- TestOwnerGrantPublicationFailureDoesNotAuthorize
- TestOwnerGrantPublicationFailureDoesNotAuthorize/file_publication
- TestOwnerGrantPublicationFailureDoesNotAuthorize/deferred_commit
- TestOwnerGrantPublicationCanonicalAncestorAlias

这些现有测试经过实际Coordinator/owner-bind IPC与Store，验证发布失败/真实deferred COMMIT失败不能授权及canonical ancestor alias产生可用capability；故障由现有夹具控制，不宣称自然磁盘耗尽或物理断电。

Go1.26.8、CGO_ENABLED=0、GOOS=linux、GOARCH=arm64 crossbuild internal/coordinator testbinary。GOPROXY/GOSUMDB关闭、GOTOOLCHAIN=local，原crossbuild command/stdout/stderr/exit均保存，exit0且stdout/stderr为空。没有重复Mac/race/fullStore检查。

复用已审owner-publication-verification/scripts/run-linux.py，只替换本任务mode、两个exact top selector及容器name prefix。实际cached image固定 `sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`；pullnever、networknone、nonroot65534、read-only、cap-dropALL、no-new-privileges、pids96、memory768MiB、cpu2；沿用/tmp128MiB noexec与/private/tmp384MiB exec tmpfs、TMPDIR=/private/tmp、只读testbinary mount。没有pull/install/network/provider/model/account操作。

原docker start --attach exit0，原State Running=false、ExitCode=0、OOMKilled=false；closed PASS set必须精确等于上述4names，无helper skip或缺失可混入通过。原container log SHA256 `c01558deaef29ce928dcf04aad81bc1664b31cf62499325945cdf2ea3b1090c2`。

清理只使用本runner创建的exact CID，finally移除后第一次inspect absent，再独立第二次exact inspect absent。随后逐项核对binary原UID/nlink=1/dev/inode/mode/size/SHA，确认原attach/state终态后精确unlink；binary absent，保留空的本任务linux-bin目录，不触及其他资源。data/cleanup.json保存完整receipt，data/summary.json保存actual counts、CID/image、raw SHA及source。

本报告只验证e50当前两个owner-bind路径的Linux行为；不宣称完整F10/F04/F01–F13、资源资格、原生Provider或用户安装验收。ROOT负责最后独立复核与集成。
