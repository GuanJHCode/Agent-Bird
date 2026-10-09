# F13 当前 F02/F10 候选包验收

clean source `4543635247b7f3a6632fcf903ea2e1cff5180594` 的 macOS arm64 打包和任务私有安装管理验收通过。ROOT 复用已独审 `3168e07` 的原 verify-package.py，没有改 runner、生产或打包脚本。Go 模块树 `0d428fa6094a594a2be2cfb423053e078bdc4f97`，版本 `0.1.0-closure.4543635247b7`。

- 固定 Go1.26.8 / Python3.14.3；离线缓存、CGO0、trimpath、buildvcs=false。实际 12 commands 的原退出码全为 0、stderr 为空：toolchain/mod verify、两次 build、build metadata、两次 package、install、exact replay、Doctor、uninstall、后置 Doctor。原外层 runner Wait=0。
- 两次构建字节一致；两个 package 的 28 文件 hash/mode 一致；独立清单核对 24 源文件。安装完整清单为 27 payload + manifest，原 binary device/inode/mtime 在 exact replay 保持；Doctor healthy、无 pins/issues，卸载后仅测试版本消失，runtime-data sentinel 保留。
- binary SHA256 `d90bfab89f6e2aab1a1c11f4eca0941e3933863b79eccfe0c6c0647f8073ea46`；package manifest SHA256 `63ec6e3000e2f3a0956c8aa1e7bce4a92764112a262d533e50146ad2a76b040a`；原 receipt SHA256 `e1d9d067eb06201b3221cf8053ed9c61cd73d3a205c7828b3cf4e538ffcd3cd3`。

原命令/请求/stdout/stderr/exit 见 [receipt](../data/owner-publication-20261009/receipt.json) 与 [ROOT 核验](../data/owner-publication-20261009/root-verification.json)。这是 12 个命令级验收，不是新增 Go test 数量；未重复 F10 功能套件。

保留 packageA 的准确路径：

`/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/capability-closure-20261006/tmp/publication-package/tasks/closure-package-provenance/tmp/owner-publication-20261009/package-a`

原 subprocess.run 和外层 runner 均已终止后，仅按 UID/nlink/device/inode/hash/mode 核验删除两份重复 build binary 与 packageB，共 64,316,844 bytes、30 files；删除前逐项意图/身份、删除后 absent 均持久化于 [ROOT cleanup](../data/owner-publication-20261009/root-cleanup.json)。packageA 再次核验未变，私有安装已通过公开 uninstall 回收；原旧包和其他 WT 未动。

当前机器 Python 的绝对路径/hash 固定，未打包独立 interpreter，不能推断其他机器可直接安装。`full-native-hooks` 是包格式；本轮没有注册 Hook、替换用户插件或调用真实 Provider/model/account。Intel Mac/Windows/Linux full-native、TUI/Hook 消费、物理断电和 F12 资源资格未由本包验证；Formal60 仍 FAILED。没有 push/release，F01–F13 整体未完成。
