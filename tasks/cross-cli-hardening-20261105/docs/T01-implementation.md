# T01 实施交接（awaiting_review）

基线 `29691e9f08468b1de56cb0c42977084b58c91975`；唯一写入树 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t01`。未提交、未推送、未修改集成树。生产变更与最终测试过程中工作树保持冻结。

## 变更与可复核位置

- `tools/orchestrator/internal/host/managed_directories.go:25`：非ASCII比较用系统 `/usr/bin/iconv -f UTF-8 -t UTF-8-MAC`；CommandContext最多2秒，输入256KiB、输出1MiB，错误失败关闭。ASCII比较不启动该进程；保留原始路径用于磁盘与精确授权。
- 同文件`:69`：先检查全清单及祖先的文件/目录冲突、重复、大小写、NFC/NFD碰撞、禁止组件和逃逸，再允许材料化。
- 同文件`:118`、`:151`：基于目录fd检查现有磁盘别名与拓扑，逐级不跟随symlink；整张清单先完成只读检查。
- 同文件`:215`、`:250`：Mkdirat只创建必要祖先，Openat(O_NOFOLLOW|O_DIRECTORY)保持根/目录fd；复验inode/device、权限与owner。对照材料化收据根身份。准备前记录intent，逐个新目录记录identity；出错只关fd，保留全部现场，不自动递归删除。
- `candidate.go:26/49/53/61/69`：全部声明在材料化前验证；workspace_prepared先落盘；目录准备接入公共候选流程；保持目录fd到worker结束，启动前与冻结前复验。
- `host.go:173/230/387`：公共managed启动路径在真正process.Start前检查目录和全部目标拓扑，拒绝准备后替换。
- `managed_policy.go:19`：Codex/Claude继续只投影原声明精确文件；Claude atomic规则与冻结范围实现均未扩大。
- `managed_directories_test.go:22/49/64/104/149/204/324/355`：8个新增顶层测试与24个子场景，覆盖父目录准备、不预造文件、声明/磁盘冲突、失败恢复、零启动、Codex与Claude真实macOS沙箱、意图失败不创建。

## 行为验收证据

| ID | 已观察的结果 | 层次 |
| --- | --- | --- |
| WS-01 | 已有CHANGE.txt与两级新目录目标；只建祖先、目标在worker前不存在。Codex 0.166.0真实native sandbox调用生产validate_host_policy/render_native_config，Claude真实Seatbelt原子临时写+rename；两者都冻结产生candidate_oid。 | unit + OS-native-local |
| WS-02 | foo与foo/bar、反序、大小写与NFC/NFD祖先碰撞、禁止组件、逃逸、已有别名、file占位、symlink均拒绝；5种非法清单+4种准备后根/目录/目标替换通过真实Host.execute证明worker标记不存在；外部目录未发生创建。 | unit + OS-native-local |
| WS-03 | 两个真实沙箱中同目录邻接、Git link/common-dir/worktree metadata、worker新建目录均拒绝；既有atomic碰撞、严格后缀、残留冻结拒绝回归也通过。 | OS-native-local |
| WS-04 | 准备intent失败不创建；首目录创建后注入journal失败，保留identity与unknown内容；显式准备恢复保留原inode且不删除未知文件。采用保留策略，不自动清理空目录。 | protocol-fixture |

## 执行命令与结果

cwd为本worktree `tools/orchestrator`。所有Go命令固定 `/private/tmp/go1.26.10-sdk/bin/go`，PATH前置该目录，`GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp`，`GOCACHE=/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/go-cache-t01`。

- 红色回归：`go test -json -count=1 ./internal/host -run 'TestCandidatePreparesMissingParentsWithoutFiles|TestManagedDeclarationConflictsRejectedBeforePreparation'`；2顶层+7子项FAIL，预期失败。`baseline.jsonl`保留。
- `go test -json -count=1 ./internal/host ./internal/gitops ./internal/gitopsworker`：退出0，3包PASS，163顶层PASS、235子项PASS、3顶层SKIP，0 fail/error。
- `go test -race -json -count=1 ./internal/host ./internal/gitops ./internal/gitopsworker`：退出0，同样3包/163顶层/235子项PASS，3顶层SKIP，0 fail/error。
- `go vet ./internal/host ./internal/gitops ./internal/gitopsworker`、`go build ./...`、`git diff --check`、修改文件`gofmt -l`均退出0且无输出。
- 补跑：设置`ORCHESTRATOR_CLAUDE_NATIVE_BINARY=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/cli-version-compatibility/tmp/npm/extract/claude-2.1.316/package/claude`后，`go test -json -count=1 ./internal/host -run '^TestNativeClaudeGateway(ReadOnly|ManagedWrite)Transport$'`；真实Claude 2.1.316 CLI对owned loopback SSE，2顶层PASS、0 skip/fail/error，无真实模型或凭据。

普通/race中的3项skip：2项原生Claude transport默认需要显式固定binary路径（后已单独补跑成功）；另1项`TestS4StageTimingObservation`为opt-in diagnostic，未启用。未将skip记为通过。

`native.jsonl`还保留了一轮测试断言失败：Codex沙箱已正确执行且退出0，但临时HOME导致PATH alias警告写stderr；测试改为单独核对stdout，未放宽产品权限。`focused-v2.jsonl`保留preSpawn钩子按值复制问题的红色证明，随后通过共享函数指针修复，最终ordinary/race均包含替换零启动测试。

## 边界与未覆盖

- 未运行真实父会话/模型请求，不声称live-provider验收；未打包/安装。这是T01局部验证，最终集成全量Go/Python/包验收由主会话负责。
- Unicode比较依赖macOS系统iconv UTF-8-MAC；非macOS Unicode场景未验证，转换不可用时拒绝。单目录最多8192项、比较输入最多256KiB，超界失败关闭。
- 延续现行稳定且让出的所属worktree合同。fd锚定避免跟随symlink，启动前复验检测已发生的替换；不宣称可原子阻止同UID外部进程在最终检查与spawn之间任意重命名目录。
- WS-04采取保守全保留策略；本变更未增加自动删除或自动重开同一已有worktree的恢复入口。未知内容、创建后落盘失败现场均保留给显式对账。
- 目前awaiting_review；独立审查和主签收尚未完成，不标done。

## 绑定与日志摘要

- 五个变更文件组合SHA256：`68da52b5b662c34fb270b22022d90881ebdd7a6f53028405d9d2067de5aca706`。
- 完整可应用补丁：`implementation.patch`，SHA256 `85acc30fa938ed5454b4342b2c432be12f5fca46e674a8ef1bc2d1a773dc4d52`。
- `baseline.jsonl` SHA256 `0352c0811c6711092a788f8a37eadd9a9b5e780f54d4e8fe1c6d4d010d8aabe0`。
- `regression.jsonl` SHA256 `ae28694a1fd208f2e1e4eb903e64fdc0b8824a03b448f50b7466d5184a9bae1a`。
- `race.jsonl` SHA256 `f56165b62f0da27e17339314445baf7ee6fec3f4c3b9105867d3f7e065e2f47d`。
- `claude-native-transport.jsonl` SHA256 `fbbaf5363f8cf109065ee039833d9480f1d0e82cdfa9c38dc0c9a847ed5c84cd`。
- `vet.log` SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。
- `build.log` SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。

完整文件SHA、CLI版本/摘要见`source-manifest.json`；全部中间结果计数见`validation-summary.json`；命令环境见`commands.json`。保留日志、补丁与必要证据，无本任务确定无用的临时文件需删除。
