# Codex Orchestrator 永久安装与激活提案

## 结论与批准对象

本提案用于审查最终的用户级永久安装和激活范围，不执行任何操作。当前 G4 已有可用的版本安装、`Doctor`、pin/unpin 和安全卸载；原生服务注册与 Codex plugin 注册仍缺生产入口，因此现在不能直接进行永久激活。

最终批准应绑定下列不可变值，不能只批准“最新版”：

- `FINAL_VERSION`
- `FINAL_PACKAGE_MANIFEST_SHA256`
- `FINAL_BINARY_SHA256`
- `FINAL_RUNTIME_MANIFEST_SHA256`
- `PINNED_PYTHON_PATH` 与 `PINNED_PYTHON_SHA256`
- `NATIVE_CODEX_PATH` 与 `NATIVE_CODEX_SHA256`

只有这些值已写入最终验收 receipt、下面列出的激活缺口已经实现并通过审查后，批准才可用于一次用户级安装和激活。批准范围不包含登录、登出、账号切换、读取、复制或修改 Claude、AGY、Grok、Codex 或其他 CLI 的认证材料，也不包含真实模型调用。

## 拟使用的固定路径

所有路径都属于当前 macOS 用户，创建的新目录必须为当前 UID 所有、真实目录且 mode 为 `0700`；现有目录不通过 `chmod` 接管。路径任一祖先为 symlink、所有者不符或权限过宽时停止。

| 用途 | 拟定路径 |
|---|---|
| G4 安装根 | `/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator Packages` |
| 固定版本根 | `/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator Packages/versions/FINAL_VERSION` |
| orchestrator binary | `<固定版本根>/bin/codex-orchestrator` |
| plugin 根 | `<固定版本根>` |
| G0 runtime | `<固定版本根>/runtime/g0` |
| G1 用户数据根 | `/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator` |
| G0 配置与 receipt | `<G1 用户数据根>/native` |
| LaunchAgent plist | `/Users/guanjunhui/Library/LaunchAgents/org.codex.orchestration.proxy.plist` |
| 原生公共 socket | `/Users/guanjunhui/.codex/app-server-control/app-server-control.sock` |
| 项目内 marketplace 根 | `<G4 安装根>/marketplace` |

安装根和 G1 数据根故意分离，因为当前安装器拒绝 `DestinationRoot` 与 `DataRoot` 相同或互相包含。G1 数据根沿用程序现有的 macOS Application Support 默认路径。卸载版本包时不删除该数据根。

## 已实现的安装阶段

最终 package 必须先在项目证据中记录 `package-manifest.json` 及上述 SHA-256。安装调用现有 binary 的 `install --request`，request 是临时 owner-only `0600` JSON：

```json
{
  "source_root": "REVIEWED_UNPACKED_PACKAGE/plugin",
  "binary_path": "REVIEWED_UNPACKED_PACKAGE/plugin/bin/codex-orchestrator",
  "destination_root": "/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator Packages",
  "data_root": "/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator",
  "version": "FINAL_VERSION"
}
```

安装器会生成并持久化以下现有记录：

- `<固定版本根>/manifest.json`：每个已安装文件的 path、mode、SHA-256 与 inode/device 身份。
- `<G4 安装根>/manifests/FINAL_VERSION.json`：绑定安装 manifest SHA-256 的 receipt。
- `<G4 安装根>/.install.lock`：由内核 `flock` 管理的稳定锁文件。
- `<G4 安装根>/pins/*.json`：运行任务的版本 pin。
- `<G4 安装根>/uninstall-intents/*.json`：卸载前先 fsync 的恢复意图。

安装完成后必须用同一已安装 binary 执行 `doctor --request <0600-json>`，其中 request 仅包含固定 `destination_root`。`Doctor` 必须确认版本、receipt、文件 hash/mode、runtime manifest、固定 Python 和 pin 状态全部健康。安装 request 用后删除；安装 receipt 和 `Doctor` 输出写入项目验收目录。

## 拟写入的原生服务配置

现有原生入口是：

```text
PINNED_PYTHON_PATH -I -B \
  <固定版本根>/runtime/g0/projectproxy_launchd_entrypoint.py \
  --manifest <G1 用户数据根>/native/config/service-FINAL_VERSION.json
```

该入口只消费 launchd 提供的名为 `Listener` 的 socket FD；它不拥有或任意删除公共 socket 路径。服务 manifest 必须是当前 UID 所有的 `0600` 普通文件，至少使用已经实现的 schema：

```json
{
  "version": 1,
  "external_admission": {"version": 1, "mode": "launchd"},
  "public_socket": "/Users/guanjunhui/.codex/app-server-control/app-server-control.sock",
  "state_dir": "/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator/native/state",
  "grants_dir": "/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator/native/grants",
  "isolation_manifest": "/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator/native/config/isolation-FINAL_VERSION.json",
  "isolation_manifest_sha256": "ISOLATION_MANIFEST_SHA256",
  "backend_argv": ["NATIVE_CODEX_PATH", "app-server", "--listen", "unix://{socket_path}"],
  "backend_executable_sha256": "NATIVE_CODEX_SHA256",
  "file_pins": {"ABSOLUTE_RUNTIME_FILE": "SHA256_FOR_EACH_PINNED_RUNTIME_FILE"},
  "idle_seconds": 10,
  "native_tui_policy": {"version": 1, "resume_owned_thread": true, "initial_history_discovery": true},
  "owner_helper": {
    "executable": "PINNED_PYTHON_PATH",
    "executable_sha256": "PINNED_PYTHON_SHA256",
    "source_path": "<固定版本根>/runtime/g0/owner_helper.py",
    "source_sha256": "OWNER_HELPER_SHA256"
  }
}
```

`file_pins` 必须逐项列出 `runtime-manifest.json` 中的已安装绝对路径和 hash，不能引用开发仓库。isolation manifest 是另一份 `0600` JSON，配置类型为 `version:1` 加 `profiles` 映射；每个 profile 必须绑定 `task_root`、`home`、`codex_home`、`workspace`、`public_socket`、`backend_socket`、`expected_executable`/SHA-256、`protected_paths` 和 `protected_read_paths`。它只能描述路径和执行边界，不能内嵌 token、cookie、Keychain 内容或 CLI auth 文件内容。

LaunchAgent plist 的内容类型已经由 G0 合同固定：

```plist
Label = org.codex.orchestration.proxy
ProgramArguments = [上述固定 Python、-I、-B、已安装入口、--manifest、固定 manifest]
Sockets.Listener.SockPathName = /Users/guanjunhui/.codex/app-server-control/app-server-control.sock
Sockets.Listener.SockPathMode = 0600
```

注册域固定为 `gui/<当前 UID>`。plist 必须以 `0600` 写入固定路径，先记录文件身份和 SHA-256，再调用一次 user LaunchAgent bootstrap。激活 receipt 至少记录 transaction ID、版本、package/runtime/service/plist SHA-256、program arguments、launchctl 返回值、job 身份、socket 身份和状态；不记录环境变量或认证内容。

## Codex plugin 注册提案

当前版本根是 G4 管理的不可变安装，不应手工改动。若要让 Codex 发现 plugin，拟在 `<G4 安装根>/marketplace` 生成一个由 manifest 管理的只读投影：

```text
marketplace/
  .agents/plugins/marketplace.json
  plugins/codex-orchestrator/
```

marketplace entry 的配置类型为：

```json
{
  "name": "codex-orchestrator",
  "source": {"source": "local", "path": "./plugins/codex-orchestrator"},
  "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
  "category": "Productivity"
}
```

这里的 `policy.authentication` 是 marketplace schema 的必填枚举，不授权或触发任何 CLI 登录；plugin 自身仍为 `capabilities: []`。注册必须使用 `plugin-creator` 的 marketplace/cachebuster/reinstall 流程，不能手工覆盖已有 marketplace 或全局 Codex 配置。投影内每个文件必须与 `<固定版本根>/manifest.json` 的对应条目相同，并单独记录 projection receipt；不能用未核验 symlink 指向活动版本。

plugin 被调用后仍通过 `${PLUGIN_ROOT}/scripts/invoke.sh` 进入同一已安装 binary。`native-bridge start` 只在已有 G0 service/owner receipt 时建立回传 driver；它不是 launchd 注册入口，也不会代替原生 `codex` 启动命令。启动时保持静默，不自动 resume 历史任务。

## 当前不能执行的真实缺口

以下项目尚未在生产包中实现，任何一项未完成都应阻止永久激活：

1. **没有生产激活/停用入口。** 当前 CLI 只有 install/doctor/uninstall/pin/unpin 和 native bridge 命令；不存在可审查的 `activate`/`deactivate` 实现。`native-bridge start` 不能注册 LaunchAgent。
2. **G0 注册事务未随 distribution 交付。** 已审查的 `activation_transaction.py` 仍位于任务实验代码，`runtime/g0` 没有包含生产 manifest builder、bootstrap/bootout controller 和恢复入口。
3. **isolation manifest 仍是实验边界。** 当前 `auth_isolation.IsolationSpec` 强制使用新的 `/private/tmp/g0-auth-*` 根，不能生成上面拟定的持久 Application Support 配置。不能通过删掉校验或复用临时 fixture 来永久化。
4. **缺少 service 级版本 pin。** 现有 pin schema 绑定任务，不能证明 LaunchAgent/manifest 仍引用某版本。激活前需新增 durable activation pin；bootout、socket/job 清理和 receipt 收口后才能释放。
5. **没有版本根到 marketplace 的受管投影。** G4 安装不会创建上面的 marketplace layout，也没有注册、cachebuster 更新或精确撤销逻辑。
6. **没有统一恢复事务。** 进程在写 manifest、plist、pin、marketplace entry 或 bootstrap 之间崩溃时，当前生产 binary 无法根据持久阶段继续或安全回滚。

最小落地步骤是：在 installed distribution 中加入一个生产 activation transaction，实现上述固定配置的 prepare/commit/reconcile/deactivate；将其写集限制为安装根、G1 数据根、一个固定 plist、一个固定 socket 和一个专用 marketplace entry；新增 activation 类型的版本 pin 和 projection receipt；把 production isolation root 改为受管的持久 0700 根，同时保留路径、owner、mode、binary/hash 与受保护路径校验；最后用临时 HOME/launchd fixture 先做可逆纵向验收。实现与最终 hash 冻结后，再把本提案中的占位符替换为真实值供用户最终批准。

## 停止、停用、卸载与记录保留

永久停用必须按以下顺序执行，并在任一步不确定时停止而不是扩大删除范围：

1. 停止接受新 submit；对该版本的任务逐项 `status`，显式 stop 活动 segment，并完成需要保留的 collect/ACK。`unknown`、开放问题、可恢复任务或 pending delivery 继续保留 task pin。
2. 由待新增的停用事务请求 native bridge driver 收口，确认 driver、helper 和已拥有的 backend 退出；现有公开 CLI 没有独立 driver stop 命令，因此实现前不能把发送任意信号当作这一步。不能只看 leader PID。
3. 由同一 activation transaction 对 `gui/<UID>/org.codex.orchestration.proxy` 执行 bootout，并核对 job 消失、service/backend process identity 退出和固定 socket identity 被安全清理。
4. 写入最终 deactivate receipt 后释放 activation pin。任何 plist/socket/manifest 身份漂移都标记 `unknown` 并保留对象。
5. 使用 plugin 管理流程只撤销 `codex-orchestrator` 的 marketplace entry/cache，不重写其他条目；保留其撤销 receipt。
6. 对 `FINAL_VERSION` 先执行 `Doctor`，再调用现有 `uninstall --request`。安装器只删除 manifest 列出的未修改文件；changed、unknown、pinned 或 `cleanup_pending` 状态全部保留并报告。

默认保留 `/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator` 下的 G1 数据库、control/owner receipt、native activation/deactivation receipt、delivery history 和 uninstall result。它们不由版本卸载器拥有。后续若要删除这些记录，需要另一个按 manifest 和 retention policy 审查的请求；本提案不授权删除。

## 最终用户看到的可审查包

激活实现完成后，提交给用户的批准材料应只有一组明确对象：

- 最终六项版本/hash/path 值。
- package manifest、安装 manifest/receipt 和 `Doctor` 输出。
- service manifest、isolation manifest、plist 和 activation pin 的完整内容与 SHA-256。
- marketplace entry、projection manifest 和 cachebuster 变更。
- prepare 阶段的拟写路径清单，以及 deactivate/uninstall 恢复步骤。
- 完整 native product 与资源验收的最终结果。

用户批准后才可写入上述永久路径、注册 marketplace 和 bootstrap 固定 user LaunchAgent。该批准不扩大到 auth、provider、模型、公开发布、许可证选择或 Windows 支持。
