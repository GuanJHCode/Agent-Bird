# F04 bounded stream executor：ROOT 验收

基础执行原语验收通过；生产/最终测试 `1ec0cb5393d95be43899db8d6a2f80cf98bfd3fc`、worker交付 `3b5aedd`、ROOT合并快照 `23543f8` 的 Go 模块树均为 `5e7ee63252d7d3062ee2259297fcd20df5666129`。它仍未开放公共 filter，也未完成 Store 资源预约、持久 broker 或 Git 双视图传播。

ROOT 实际解析最终 clean-source race：4 top / 25 named PASS、0 FAIL/SKIP、exit0、stderr空，原输出 SHA `0652fbd0ac0a1a70d90808ae88ad879827585ca4d80857467b0630c996507f8b`。其中新 gitfilter 3/17，原 F01 sealed-tool 代表性回归1/8；源码hash逐项核对，8个任务私有fixture根再次确认absent。未重跑worker测试。真实RED和全部中间证据见[worker报告](../../filter-stream-broker/docs/final-validation.md)。

ROOT 以 clean `7c7ce9e` 隔离执行 whole `go build ./...` 与 gitfilter/process/Host vet，实际 exit0且stdout/stderr为空；Linux arm64仅 `go build ./internal/gitfilter` exit0，证明 unsupported leaf可编译，不是Linux filter执行资格。固定生产/测试独立审查PASS；native parser原样抽取、Host只委托。原始command/env/exit及根目录复核见[root-receipt.json](../data/root-receipt.json)。

只接受可信caller已seal的native工具、只读emptydir、独占input/outputFD；输入/output各16MiB、tool32MiB、进程deadline最多60秒。真实native转换、输出上限、非0exit、cancel和部分Start错误保留原Wait/tree；policy有正向对照及真实other-binary in-place exec拒绝。未强造真实kernelUNKNOWN，未资格化same-tool selfexec循环、资源/RSS、其他平台、LFS、hooks/signing或完整公共候选链；这些限制见worker用法说明。没有真实Provider/model、用户安装替换、push或release。
