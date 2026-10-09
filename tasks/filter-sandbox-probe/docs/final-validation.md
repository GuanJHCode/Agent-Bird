# F04 Darwin filter 隔离前置证据

固定测试源码 `5c10ca05704c36f7b6d86fb1cd13fc0988d7730d`，基线 `9552f0e`。仅新增测试、compiled actor 和调查脚本；没有生产行为修改，候选 filter gate 继续拒绝。

macOS 26.3.1 arm64、Go 1.26.8：唯一 native matrix **1 top / 3 named PASS，0 FAIL/SKIP**，exit 0；gitops 和 actor 定向 vet exit 0。独立固定快照审查 PASS，ROOT 解析原始 JSON、exit、hash 和清理状态。没有增加全量测试或重复 race：本批是隔离可行性证据，未修改既有并发实现。

positive control 实际允许 26 项操作，排除路径不存在或 listener 未建立；isolated smudge/clean 在固定 Seatbelt 内完成二进制 stdin/stdout 转换，精确恢复原字节，同时验证：

- 候选、common Git、Host state、外部任务 sentinel 的内容读取、目录 listing、写入被内核拒绝，原 sentinel 字节不变。
- hardlink、经 scratch symlink 的越界内容访问、fork/exec、TCP loopback 和 Unix socket 连接返回底层 EPERM/EACCES；两个 parent listener 均无连接。
- 全局文件 metadata、根目录 `/` 本身 listing、列明的 OS runtime 路径读取和 private scratch 读写明确允许。没有宣称隐藏全部文件存在性或禁止全部元数据访问。
- 编译 actor 使用 pinned canonical Go、精确离线环境、任务私有 HOME/cache/GOPATH/TMPDIR。build 和 3 个 actor 都有原 process.Handle、原 Wait 0、tree 确认；actor 与 sandbox-exec 摘要复核一致。

最终 raw：`data/native-final/stdout.jsonl` SHA-256 `e956ef8d507bdf0b7083d92a73db434c2c8cefe98dea204a105b87e37b8999d5`；stderr 为空。原始 receipt 保存 command/source/toolchain/OS、准确具名结果。actor SHA `4cb7e01efcb012a0ed726edff29872d9068dc495fef8bdaa9b49861aac11c2e1`；`sandbox-exec` SHA `f3162ae11789a5b296bb3850d493c33ddd52053a03f984b2c4bc34004f4fee99`。

保留失败：native-01/02 都是 positive control PASS、isolated 在 main 前 SIGABRT。原 PID68780 的自身 crash report 指向 dyld CacheFinder/ignition；完整 OS 报告留在系统原目录，没有复制设备/事件标识到交付物。随后 `data/loader-01` 用同一个 `/usr/bin/true`、同一 policy，仅加入 `file-read-data (literal "/")`，原 Wait 从 -6 变为 0；这只定位本机 loader 所需的字面根目录读权限，不证明每个系统版本。native-03 已运行完两次转换，但测试使用的 os.IsPermission 未穿透 net.OpError，4 条 errno oracle FAIL；改成 errors.Is(EACCES/EPERM) 后最终通过，所有旧 raw 均保留。

清理：四批 fixture 的原 actor 已终态，测试只删除各自私有 fixture。ROOT `data/root-cleanup.json` 再次确认 4 个精确根目录均 absent，ps 中无引用这些根的 actor；无 PID 探测信号、无用户安装变动。保留全部原始证据和调查脚本。

**未完成/未验证**：公共候选 Git filter/LFS/hooks/signing、Git 父子权限拆分、真实 Git round-trip、manifest/receipt/continuation/compose/view 传播、入口二进制发布竞态、资源资格、其他平台。这里是固定 native actor 的 OS 前置证据，不是 F04 完成。F01 Host/action 契约稳定后，才接完整公共链。未调用真实 Provider/model/account，未替换安装、推送或发布。
