# F13 当前候选包复验：validation-start-20261009

受限包溯源与隔离管理验收PASS。执行前后source均为clean `3bd025e9816c95c6a06d5fe775e25b19199b73cb`；这里只验证该快照的macOS arm64打包、独立source closure和task私有安装管理，不宣称全部F01–F13或资源资格完成。

未改生产、打包脚本或既有runner。复用最后独审固定 `3168e0752539768d89aaba4b579aa7b74afd9d0a` 的verify-package.py，在新 `validation-package` worktree、branch `test/validation-package-20261009` 运行新label。data/tmp父目录创建为空且当前用户owned0700；旧closure-package-provenance worktree及其packageA未动。

## 身份与实际执行

- source tree：`b87fba7b5fb203676b5f073c76a50d3fff326cd4`
- orchestrator tree：`5bd9e03e660b8b5ab8f4197bc8fcf22ce4878729`
- version：`0.1.0-closure.3bd025e9816c`
- binary SHA256：`c9a09e7c1bde01c20145294ed2f702a15b91bb10138908fadb6cf951470628ba`
- package manifest SHA256：`9e4065cce26f8e8ce7045ca7d3e7803a6a156d68e50d7c5c8f99755d3caf0154`
- installed manifest SHA256：`fb2e589f91a4a3e75ac5c5ad840a079bcfdf89147b51d41a21e1e908083bedc4`
- Python SHA256：`760d13aa43656d9e51bcbc1847088691a035f9dc37030070f1d4993f55d4e689`

工具是Go1.26.8 darwin/arm64及指定Python3.14.3。Go build显式trimpath、buildvcs=false、CGO0、GOOS darwin/GOARCH arm64/GOARM64 v8.0；GOENV/GOWORK off、GOFLAGS空、GOTOOLCHAIN local、GOPROXY/GOSUMDB off，依赖只用已存在cache且go mod verify通过。原command完整argv/环境及Go build metadata见raw receipt；外层runner约10.32秒，exit0，无stderr。

实际12commands（全部exit0、stderr空；这是命令级验收，不是Go test数量）：

1. go-version
2. go-mod-verify
3. build-a
4. build-b
5. go-build-info
6. package-a
7. package-b
8. install-first
9. install-replay
10. doctor-installed
11. uninstall-owned
12. doctor-after-uninstall

两次独立build bytes相同。正式package-plugin.sh --kind full-native-hooks执行两次，全部28package files bytes/modes相同。独立固定清单核24原source files、唯一plugin version改写、binary及生成的runtime/payload manifests；不只相信package自报manifest。

私有install严格27payload files+1installed manifest=28files，无额外文件/空目录、symlink或hardlink、UID/mode符合预期。Exact install replay返回完全相同receipt，binary device/inode/mtime保留。已安装Doctor healthy且仅预期version，pins/issues为空。Uninstall只移除该测试版本，独立runtime-data sentinel保留；后置Doctor healthy，versions/pins/issues为空。

原始receipt、12组stdout/stderr/exit、5份admin request以及独立worker核对见 [data/validation-start-20261009](../data/validation-start-20261009/receipt.json)、[worker-verification.json](../data/validation-start-20261009/worker-verification.json)。外层runner command/stdout/stderr/exit另在data父目录同label前缀文件。本次无FAIL、timeout或覆盖旧证据。

## 产物与清理

保留packageA：

`tasks/capability-closure-20261006/tmp/validation-package/tasks/closure-package-provenance/tmp/validation-start-20261009/package-a`

该路径相对用户outer仓库；receipt记录完整绝对路径。Raw证据、安装receipts与runtime-data/keep.txt保留，私有测试版本已由公开uninstall移除。

全部12原subprocess.run Wait及外层runner原Wait已完成，exact工作路径无外部actor引用。重新核UID/nlink/dev/inode/hash后，只删除两个duplicate build binaries与28文件packageB，共 **63,784,140 bytes**；packageA逐文件hash/mode再验仍相同。未signal任何PID，未删除其他worktree/旧包。详见 [worker-cleanup.json](../data/validation-start-20261009/worker-cleanup.json)，精确removed paths均不存在。

## 边界

full-native-hooks是包格式名，本轮不注册或替换用户Hook/插件安装，不构成该候选binary的实际TUI/Hook消费或真实Provider/model/account资格。当前机器Python路径/hash被pin，未打包独立interpreter，不宣称另一台机器可直接安装。无push/release、生产操作或用户配置替换；未重新验证Linux full-native/Intel Mac/Windows、资源Formal60或整个能力计划，旧资源测量不能资格化该binary。候选只绑定上述source，不自动覆盖后续源码。
