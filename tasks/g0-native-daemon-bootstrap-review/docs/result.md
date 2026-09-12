# 官方 0.154 daemon 安装与普通入口：只读结论

固定官方源码 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`（`rust-v0.154.0`）。存在无需 TUI 代理的官方后台启动命令；**没有在本次核查的 macOS daemon 管理路径中发现登录、开机或普通 TUI 缺失 listener 时自动拉起的注册机制**。安装/Setup Skill 可以把内建命令作为一次显式初始化动作，但这只为后续打开准备一个现存 listener，不能单独完成全部冷启动和退出生命周期合同。

此前 [socket activation 调查](../../g0-pending-resolution/docs/idle-socket-activation-mechanism.md) 核查的是 0.153.4 的 TUI 探测、listener FD 和自建 launchd 前端；没有覆盖这里的 `app-server-daemon` bootstrap 实现。[既有 daemon 实验](../../../docs/implementation/g0/daemon/result.md) 也明确未运行 bootstrap/start/stop/restart，使用的是任务自建原生 listener。本次补的是这条具体内建路径，没有重做代理方案。

## 内建命令的实际语义

CLI 的 `codex app-server daemon` 包含 `bootstrap [--remote-control]`、`start`、`restart`、`stop`、`version`、`enable-remote-control`、`disable-remote-control`，另有内部 `pid-update-loop`。定义与分派见 [cli/main.rs:780](../../g0-native-0154-contract/data/source/codex-rs/cli/src/main.rs#L780) 和 [main.rs:1366](../../g0-native-0154-contract/data/source/codex-rs/cli/src/main.rs#L1366)。

- `start`：探测已有服务；已有服务返回 `AlreadyRunning`。否则检查 managed standalone 安装，创建后台 app-server，等待 WebSocket initialize 能返回版本。它不安装 OS 自启动项，也不启动更新器。[start](../data/source/codex-rs/app-server-daemon/src/lib.rs#L313)
- `bootstrap`：检查 managed standalone；拒绝接管已探测到的非本管理器服务；写 `$CODEX_HOME/app-server-daemon/settings.json`，必要时停止原 managed 服务，再启动 app-server 和更新器；返回 `auto_update_enabled=true`。未传 `--remote-control` 时写入 false，并非保留原值。[bootstrap](../data/source/codex-rs/app-server-daemon/src/lib.rs#L616)
- 真正的 backend 只有 `Pid`。Unix 启动执行 `setsid`，stdin/stdout 置空、stderr 写日志，运行 managed `packages/standalone/current/bin/codex app-server --listen unix://`。remote control 为 false 时还设置内建禁用环境变量。这是原生服务本身，无转发前端。[backend](../data/source/codex-rs/app-server-daemon/src/backend/mod.rs#L14)、[spawn](../data/source/codex-rs/app-server-daemon/src/backend/pid_start.rs#L93)、[argv](../data/source/codex-rs/app-server-daemon/src/backend/pid.rs#L336)、[managed path](../data/source/codex-rs/app-server-daemon/src/managed_install.rs#L18)
- `bootstrap` 的所谓 durable management 在这版代码中仍是 PID 文件和脱离终端的两个进程。更新器首次等 5 分钟，随后每小时调用官方 standalone 安装器；只对仍运行的 managed daemon 进行版本相关重启，`NotRunning` 不启动服务。重启系统后 PID 文件不会自行变成进程。[update loop](../data/source/codex-rs/app-server-daemon/src/update_loop.rs#L34)、[restart-if-running](../data/source/codex-rs/app-server-daemon/src/lib.rs#L379)

## 冷启动与 Hook 时序

1. 内建 `start/bootstrap` 的冷启动发生在显式调用管理命令时。若安装流程先成功完成该命令、再打开目标 TUI，则 listener 已存在，普通 `codex` 可直接连接。`wait_until_ready` 做 UDS + WebSocket + initialize 探测；不是只看到 PID。[readiness](../data/source/codex-rs/app-server-daemon/src/lib.rs#L478)、[probe](../data/source/codex-rs/app-server-daemon/src/client.rs#L35)
2. 普通 `codex` 自身仍只以 50ms 预算探测默认 socket，然后一次选择 LocalDaemon 或 Embedded。缺失 listener 不触发 daemon `start`。唯一已查到的 CLI 预先 `Start` 分支要求 `interactive.agents_overview && remote.is_none()`，对应 `codex agents`，不能拿它代替用户要求的普通入口。[startup selection](../../g0-native-0154-contract/data/source/codex-rs/tui/src/startup_orchestration.rs#L193)、[agents pre-start](../../g0-native-0154-contract/data/source/codex-rs/cli/src/main.rs#L2715)
3. `SessionStart` 在已有 Session 初始化、MCP runtime 安装及历史记录之后排队；执行时接收已有 session/turn context。由它启动新 daemon 已晚于该 TUI 的 target 选择，不会把当前 Embedded TUI 迁移过去。模型执行 Setup Skill 也已经位于所选服务的会话中。[queue](../../g0-native-0154-contract/data/source/codex-rs/core/src/session/session.rs#L1604)、[dispatch](../../g0-native-0154-contract/data/source/codex-rs/core/src/hook_runtime.rs#L124)
4. 已查 CLI plugin install、local/remote manager 和 manifest：安装流程材料化 bundle、安装 cache、启用插件及鉴权/缓存更新；manifest 的 hooks 是会话事件配置。这里没有通用 install/post-install daemon 启动脚本字段或调用。不能把“插件安装完成”直接等同于 `daemon start` 已执行。[CLI install](../data/source/codex-rs/cli/src/plugin_cmd.rs#L140)、[local install](../data/source/codex-rs/core-plugins/src/manager.rs#L2193)、[remote install](../data/source/codex-rs/core-plugins/src/remote_mutations.rs#L85)、[manifest](../data/source/codex-rs/core-plugins/src/manifest.rs#L47)

## 精确缺口

- **首次安装：** Setup Skill 可显式调用原生命令，随后重新普通打开可利用现存 listener；当前已经打开的 Embedded 会话不能自动换服务。原生 plugin install 本身没有上面的启动步骤。
- **后续普通打开：** daemon 仍活着且 CLI launch config 可复用时才隐式接入。daemon stop、崩溃或系统重启后，普通入口仍会走 Embedded。更新器不补这一缺口；没有预监听或 OS 自启动注册。
- **连接失败：** 0.154 与旧 socket-activation 报告不同：选中 LocalDaemon 后若真实连接失败，现在会改建 Embedded。不能以 50ms probe 成功推定最终连接共享服务。[fallback](../../g0-native-0154-contract/data/source/codex-rs/tui/src/lib.rs#L514)
- **配置与时窗：** 共享 daemon 不接纳当前调用的全部 launch overrides；存在覆盖、strict/non-replayable 配置等仍会选 Embedded。管理命令有最长 75 秒 operation-lock 等待、10 秒 readiness 阶段及内部 probe 预算，不是一个整体 10 秒冷启动保证。[reuse eligibility](../../g0-native-0154-contract/data/source/codex-rs/tui/src/lib.rs#L988)、[timeouts](../data/source/codex-rs/app-server-daemon/src/lib.rs#L32)
- **退出生命周期：** UDS TUI 断连不会触发仅 stdio 模式的服务退出路径；`daemon stop` 操作共享服务，不等于目标 TUI 的 turn/worker 停止。bootstrap 的更新器也没有随 `stop` 一起关闭。当前 TUI→thread 正证据、切换失效、退出停止执行、完全空闲退出后的下次普通打开仍需独立成立。[connection close](../../g0-native-0154-contract/data/source/codex-rs/app-server/src/lib.rs#L1042)、[stop](../data/source/codex-rs/app-server-daemon/src/lib.rs#L435)

核查范围：固定 0.154 CLI dispatch、TUI probe/target/fallback、SessionStart 排队/分派、daemon lib/client/backend PID/Unix spawn/settings/managed path/updater，以及 plugin CLI/local manager/remote manager/manifest。未安装、未执行 daemon/native、未发 RPC、未改配置或运行源码，未改 main HEAD。新保留源码与下载摘要见 [source-receipt.json](source-receipt.json)；urllib TLS 校验失败后改用保留正常证书校验的 curl 成功读取，未采用跳过 TLS 校验。没有本任务临时文件待清理。
