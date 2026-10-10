# Linux 写前 root binding 增量

固定生产/测试source `df558ee4f0add520179bcafc2b8fc21a4bbc394a`，在已交回WT获重新唯一写入权后merge；不编辑生产/测试。新runner固定 `cbfff3b`，其tools/orchestrator tree与df5精确一致，source/tree/逐文件hash、Go版本及命令在data/bind-root-02/source.json等。原e50 Linux 2top4named证据完整保留，逐raw hash未变化确认在新增summary.json。

全新label `bind-root-02`，实际仅一次Linux，**3 top / 5 named PASS，0 FAIL/SKIP**：原TestOwnerGrantPublicationFailureDoesNotAuthorize及file_publication/deferred_commit两sub、TestOwnerGrantPublicationCanonicalAncestorAlias，以及新增TestOwnerGrantPublicationRetargetBeforeWrite。新增case在callback取得合法authority后切换祖先alias，验证held root identity写前边界及replacement不能先生成owners；本报告不重复宣称原Mac RED或race由本worker执行。

Pinned Go1.26.8、CGO0、GOOSlinux/arm64 coordinator testbinary crossbuild exit0/stdout/stderr空；GOPROXY/GOSUMDBoff、GOTOOLCHAINlocal。沿原已审runner机制，cached image完整SHA `6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`，pullnever/networknone/nonroot65534/read-only/cap-dropALL/no-new-privileges，原cpu2/memory768MiB/pids96及/tmp128MiB noexec、/private/tmp384MiB exec tmpfs不变。只读binary mount；没有安装/pull/provider/model/用户资源操作。

容器closed PASS set精确等于5names，没有helperSkip或漏选混入。原attach返回0、State Running=false/ExitCode0/OOMKilled=false；原log SHA256 `dc6bd04d1374e92a9ec6cd95a16d5c1b020708d2389fa61b0247528caf59cb4c`，raw/receipt存data/linux/bind-root-02*。

exact CID finally移除后inspect absent，又独立二次inspect absent。确认原attach/state终态，重新核新binary的UID/nlink=1/dev/inode/mode/size/hash一致后精确unlink，binary absent；空task tmp/bind-root-02目录保留。完整CID/binary hash/metadata和清理事实在data/bind-root-02/cleanup.json；原binary、CID及e50证据未覆盖或重复操作。

只限定df5这组三个owner-bind Linux路径；不宣称全F10/F01–F13或资源资格。不重复Mac/race/fullStore。ROOT最终独立复核与集成；本任务回交后不继续写WT。
