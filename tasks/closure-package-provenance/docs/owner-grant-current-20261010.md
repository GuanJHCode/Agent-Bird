# F13 owner-grant 修复后的当前候选包

clean source `e662aaa0e2a98f65ad16f30871dc8f03ce73e3a1` 的 macOS arm64打包及私有公开安装管理验收通过。publication-package WT先核clean并仅fast-forward到该source；Go tree `09988866eb0bbe35b13e151559605fbbebbce150`。复用原已审 `3168e07` verify-package.py，未修改runner、生产、测试或打包脚本。

新label `owner-grant-current-20261010`，版本 `0.1.0-closure.e662aaa0e2a9`。Pinned Go1.26.8及原receipt指定Python3.14.3，offline cache、CGO0、trimpath、buildvcs=false，完整tool/env记录在本label receipt。

实际 **12 commands全exit0、stderr全空**：Go版本/mod verify、两次build、build metadata、两次package、私有install、exact replay、Doctor、uninstall、后置Doctor。外层原runner Wait=0。两次binary bytes一致、两个package **28files hash/mode一致**，独立固定closure核对 **24源文件**。安装精确27payload+manifest，exact replay保持原binary device/inode/mtime及完整清单；Doctor前后healthy、无pins/issues，卸载仅移除本测试版本，runtime-data sentinel保留。这是命令级验收，不是新增Go测试数。

- binary SHA256：`7e78c7a1a0c77af75c6040b296f620e43cc8f9acd8b0626dff79892096704782`
- package manifest SHA256：`cf883945e0b0ea596c2ee14b7b4f91a96d0b4bc76bebedf3bb5b61b1f867cd2f`
- 原receipt SHA256：`ddf71440082c0fa0fc0434d2bc0eaab664e4dfeb81a297f784a107cd184522b2`

所有原command/request/stdout/stderr/exit和完整receipt在data/owner-grant-current-20261010/；verification.json另记实际commands/count/hash/mode、原runner终态、独立closure及清理。未重复F01/F02/F04/F10 feature测试。

保留本次packageA：

`/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/capability-closure-20261006/tmp/publication-package/tasks/closure-package-provenance/tmp/owner-grant-current-20261010/package-a`

原12个subprocess及外层runner已终止后，只删除本次重复的2build binary与packageB：**30files、64,371,852bytes**。预先写入并file+parent fsync cleanup-intent.json，逐项记录/复核原UID/nlink/device/inode/mode/size/hash，精确unlink/rmdir；exact work路径无进程引用。cleanup.json证明duplicate files与packageB absent。本次packageA再次核验全清单未变，旧source454 packageA全28files同原receipt未变；旧包、旧raw和其他WT未动。原outer runner stdout/stderr/exit同时保留。

本包只限定当前源码的macOS arm64候选交付。full-native-hooks是包格式，不代表Hook注册、TUI消费或真实Provider/model/account验收。Python仅固定本机现有绝对路径/hash，没有独立interpreter bundle，不宣称其他机器可直接安装。没有用户安装/配置替换、Hook注册、push/release。Intel Mac、Windows、最新Linux full-native、真实断电、资源/RSS资格未由本包验证；旧资源结果不资格化此binary，F01–F13整体及完整F04仍未完成。ROOT最终复核并集成本报告。
