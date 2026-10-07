# F07 Linux collect-only 安装验收

本子阶段开放 Linux `plugin-run ... -- check`：严格验证 `collect-only-hooks` 包后进行私有安装和 Doctor。其他 action 继续拒绝；Darwin 原 action 集合保持。Linux Provider、sandbox、完整 native runtime 和分发发布均不在本次完成范围。

产品源码基线为 `ad62be9e5eef51c52bb2ccc8c0ae631ea341507e`，独立 security review 为 PASS。ROOT 另修正并增加本机/容器验收，源码摘要及实际计数见 `../data/root-receipt.json`。

## 已执行

- macOS arm64：完整 `internal/install`，109 top / 345 named PASS，0 FAIL；1个显式 helper 测试 `TestC11InstallPublicationCrashHelper` 在没有子进程环境参数时 SKIP。包终态 PASS，39.024秒。
- macOS arm64：相关 `cmd/orchestrator` portable policy / allowlist，3 top / 9 named PASS，0 FAIL/SKIP。
- `go vet ./internal/install ./cmd/orchestrator` 退出0。
- Docker Linux arm64：实际 Linux CLI 二进制经正式 `package-plugin.sh --kind collect-only-hooks` 打包；原生 acceptance 1 top / 1 named PASS，0 FAIL/SKIP。不仅构建或检查退出码：验证公开 check JSON、独立 Doctor、每个已安装文件的 hash/mode/UID、0700目录、重复安装 inode/mtime 不变、非 check action在任何写入前拒绝、不合法/full-native kind 在安装目录创建前拒绝。check 没有创建运行时 state。
- 容器使用固定本地镜像、无网络、只读根、no caps/no-new-privileges、UID65534和独立/tmp。宿主输入为只读归档，由原生测试解包成当前UID的0700/0600文件，以满足产品ownership规则；没有放宽产品校验。每次 CLI child 都经原 `exec.Cmd` 的 `CombinedOutput`/Wait 完成。
- 最终容器 `f6871050c1b4462f9f5e2350ac81a0f59f929248cc1100ede098eec3ac004fec` exit0、非OOM、精确rm成功，随后inspect确认不存在。完整命令、镜像、binary/harness/archive摘要和日志见 `../data/linux/root-native-v2-receipt.json`。

## 失败与边界

- worker 的两份25秒完整 suite记录均为真实FAIL，未计入通过。另三组默认10分钟cmd suite因外层返回后仍运行而被遗漏；ROOT核对其PID/birth/cwd后确认它们自然超时退出。明确归属的孤儿serve PID67512由ROOT精确SIGTERM回收；没有其原Wait成功证据，见 `../data/root-leftover-cleanup.json`。后续窄化检查未见该时间窗AGY/Claude/go-build测试残留。
- ROOT第一次Docker测试正确完成安装，但错误地要求check创建runtime state，故测试FAIL。修正为检查state不存在后重新执行；原失败保留在root-native-v1证据，不能视为产品RED。
- 原worker容器脚本既不检查安装语义，也无法使宿主UID文件通过nobody ownership验证；最终脚本用原生测试和当前UID副本解决这两项验收问题。
- 本次没有完整CLI套件通过声明；已有两项F10死owner后resume缺口仍开放。没有调用真实Provider、替换用户安装或发布。
