# 最小合成 Host 验证

日期：2026-09-11。分支：`exp/g0-synthetic-host`；基础提交：`6169c5d`。实现与测试仅位于 `tools/g0-return-lab/`。这是 G0 实验共用的模拟执行者；下列通过不代表原生 Codex 返回通道成立，也不代表 G0 通过。

## 合同与实现

CLI 合同见 [`tools/g0-return-lab/README.md`](../../../../tools/g0-return-lab/README.md)。要求显式传入预建的绝对路径、当前有效用户所有的 `0700` 空目录和安全 nonce。`start` 返回 worker PID、nonce、元数据创建时间及固定产物名称；worker 通过 `setsid` 脱离调用终端、继承正常环境与执行限制、标准流连接 `/dev/null`。没有 shell、环境持久化、权限绕过或个人配置修改。

首次启动用独占 claim 防止重复启动。子进程发布并同步 `job.json` 后通过私有 pipe 发出 ready；父进程核对实际子 PID、nonce、创建时间和已可读的磁盘内容，才返回成功。ready 检查最多五秒；已启动但不能确证时返回 `start_uncertain`，不虚报新 worker 成功，不自动重试。

worker 延迟 `100ms`–`60s` 后只尝试发布一次固定 `count=1` 结果并退出；通过独占暂存文件与硬链接发布拒绝覆盖任何已存在的目标。`read` 立即读取，`wait` 每 100ms 以内最多一次检查，超时范围为 `100ms`–`120s`。文件为 `0600`，读取核对类型、所有者、权限、长度、schema 和 nonce，拒绝符号链接，不输出外来内容。

## TDD 证据

先建立仅返回 `not_implemented` 的可编译入口和真实 CLI 测试，再执行测试，观察到以下行为测试退出 `1`：

- `TestCompletionAfterStartCallerExits`：`start` 退出 `2`，期望 `0`。
- `TestWaitTimeoutDoesNotStopWorker` 与 `TestKilledWaiterDoesNotStopWorker`：尚不能启动独立任务。
- `TestDuplicateStartAndNonceMismatch` 与不可信结果/禁止覆盖用例：尚无任务或归属记录。
- 参数测试补充固定 `invalid_args` 断言后单独运行，因返回 `not_implemented` 而退出 `1`。

随后实现最小 CLI，首轮 `go test -count=1 -v ./...` 返回 `0`，输出 `ok g0-return-lab 7.710s`。最后补充 worker 完成后在观察窗口内退出的断言及 README 后，重新执行以下必要验证。

## Host 实现验证（`0fc9b0b`）

工具链：`go version go1.26.8 darwin/arm64`，使用已有 `tasks/g0-macos/data/go/bin/go`，没有联网安装依赖。测试中的 CLI 是独立构建的真实可执行文件，worker 是实际 macOS 进程，没有模拟 I/O 或进程 API。

命令工作目录为 `tools/g0-return-lab/`。`GOCACHE` 指向本模块 `tmp/cache/`；`GOTMPDIR` 与测试 `TMPDIR` 指向本模块 `tmp/build/`。以下 `go` 均为上述已验证工具链的绝对路径。

| 命令 | 退出码 | 最终证据 |
| --- | --- | --- |
| `go test -count=1 -timeout=60s -v ./...` | `0` | `PASS`；`ok g0-return-lab 7.579s` |
| `G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=90s -v ./...` | `0` | `PASS`；`ok g0-return-lab 8.495s`；CLI 子进程也使用 `-race` 构建 |
| `go vet ./...` | `0` | 无诊断 |
| `go build -o data/g0-return-lab .` | `0` | `Mach-O 64-bit executable arm64` |

最后一次实现源码修改之后的原生二进制 SHA-256：`cf7bfc0acf6f3348210c50c75f9a0ea85b6a56e1abafbbcec85156580dc40cec`。

最终测试覆盖：

- `start` 调用者进程实际退出后，worker 具有独立进程组，先返回 pending、后落盘 completed；元数据先于成功 stdout 可读；worker 完成后退出；重复 read 不改 inode 或修改时间。
- `wait` 超时退出 `3`、被杀死的等待者退出后，原 worker 都继续完成。
- 同 nonce、不同 nonce 对已占用目录重复启动均失败；读取其他 nonce 失败且明确返回 `nonce_mismatch`。
- 延迟/超时上下界之外、非法 nonce、缺失/相对/不安全/符号链接目录、无效参数均拒绝；无效延迟不产生任务文件。
- 外来 nonce、公开权限、额外字段、非法 count、非法 JSON、符号链接结果均被拒绝；错误输出不回显输入。既有目标拒绝覆盖的确定性证据由下述审查修复补强。

## 独立审查后的测试修复

审查确认原有禁止覆盖测试存在竞速弱点：先启动延迟 200ms 的 worker，再用可截断的 `os.WriteFile` 写伪造结果。如果 worker 已先完成，测试自身会覆盖真实结果，之后仍可能通过。该观察不能作为禁止覆盖的可靠证据。

修复只修改测试与本记录，生产 `main.go` 与已交给原生运行者的二进制保持 `0fc9b0b` 内容。用例改为独占创建合法的静态 job fixture 和预先存在的目标，不再启动竞速 worker；随后直接对真实 `os.Root` 调用生产 `publish` 函数，检查返回 `EEXIST`、原 inode 和内容未变化。符号链接用例额外确认链接及其目标都不变。最后仍通过真实 CLI 验证不可信结果被拒绝。fixture 使用 `O_EXCL`，自身不能截断任何已存在文件。

在独立临时模块中，仅将 `publish` 的 `root.Link(stage, name)` 故意替换为会覆盖目标的 `root.Rename(stage, name)`，验证新测试能识别错误；没有改动交付实现。该 mutation 运行退出 `1`，五个普通文件用例均报告 `publisher overwrote an existing result`，符号链接用例报告 `publisher replaced an existing symlink`。

| 修复后验证命令 | 退出码 | 证据 |
| --- | --- | --- |
| 隔离 mutation：`go test -count=1 -timeout=30s -run TestExistingResultIsNeverOverwrittenAndUnsafeResultsAreRejected -v ./...` | `1`（预期） | 六个子用例全部因目标被覆盖/替换失败；`FAIL g0-return-lab 1.463s` |
| 正常源码：`go test -count=1 -timeout=60s -v ./...` | `0` | `PASS`；`ok g0-return-lab 5.867s` |
| 正常源码：`G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=60s -run TestExistingResultIsNeverOverwrittenAndUnsafeResultsAreRejected -v ./...` | `0` | 六个子用例通过；`ok g0-return-lab 2.469s`；CLI 子进程同样使用 `-race` |

再次核对 `main.go` 与 `0fc9b0b` 无差异；交付二进制 SHA-256 仍为 `cf7bfc0acf6f3348210c50c75f9a0ea85b6a56e1abafbbcec85156580dc40cec`。没有重新构建或替换正在被原生实验使用的二进制。

## 能证明的边界

成功 `start` 仅证明一次实际子进程启动以及 nonce/PID/元数据创建时间的一致记录；`created_at` 是写入元数据时的时间，不是系统级进程创建身份，不能单独证明后续存活、PID 未复用或跨重启身份。`count=1` 是固定合成数据，不能证明生产任务计数、交付确认或恢复幂等。

`setsid` 与普通终端退出的本地测试不能证明原生 Codex 工具一定允许派生进程、也不能证明其关闭时不会主动终止整棵进程树。原生运行者必须在真实入口单独实测。私有目录不抵御同一用户恶意伪造符合 schema 的文件；本工具没有生产恢复、重启重放、掉电持久性验证或第三方 CLI 调度能力。

## 临时资源

测试 harness 自动移除每次测试目录和临时 CLI 可执行文件。任务结束时删除本模块 `tmp/build/`、`tmp/cache/`；审查修复后同时删除隔离 mutation 模块 `tmp/overwrite-mutation/`。保留 `data/g0-return-lab` 作为原生运行者的实验交付物。没有删除其他任务数据、修改可信 Hook 二进制或个人配置。
