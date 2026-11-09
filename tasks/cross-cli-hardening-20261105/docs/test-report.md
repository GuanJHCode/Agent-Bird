# 本轮测试记录

T00 基线：source_commit=29691e9f08468b1de56cb0c42977084b58c91975，生产源码无变更。Go 1.26.10/macOS26.3.1 arm64；PATH显式包含私有Go，GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp；独立GOCACHE见baseline。命令在tools/orchestrator：`go test -count=1 -json ./internal/adapter ./internal/store ./internal/host`，exit0，132.345秒；696个Go pass测试项（含subtest），0 fail，4 skip。初始证据脚本的errors=0仅指包级失败数，不是独立错误诊断数。

公开摘要 data/public-evidence/T00-go-baseline.json；原始日志保留在外部任务 tmp/t00-evidence/T00-go-baseline.log，SHA256 `028f89b6ee61556bd6f0c3fc6632704b03897549e52b51e112cf7c0301bb2483`。不把skip算PASS、不以此代替最终树或live验证。

跳过项：
- `TestProbeCauseDecompositionObservation`
- `TestNativeClaudeGatewayReadOnlyTransport`
- `TestNativeClaudeGatewayManagedWriteTransport`
- `TestS4StageTimingObservation`

原始日志的各skip输出保留其原因，后续最终回归按实际环境单列。

## 2026-11-09 最终离线回归

冻结包源7b，149生产文件与验收树一致。Go normal/race各1654pass（656top/998sub）、0fail、8既有opt-in skip，vet/build通过；源码raw日志hash见[T08 root](../data/public-evidence/T08-short-references-root.json)。同`.4`已安装Go的Python301pass、0fail/error、7native opt-in skip；JUnit及跳过项见[REG02](../data/public-evidence/T12-regression-r4-root.json)。其中1个原有Python3.14 SyntaxWarning未作无关修改。Codex/Claude无网络metadata各8tools/sourceproof一致，无模型turn、无owned进程组残留；不是live。PKG源码/包/receipt及5负例见[.4 root](../data/public-evidence/T11-package-r4-root.json)。历史.3真实双向失败仍保留；新.4真实模型试验NOT_RUN。
