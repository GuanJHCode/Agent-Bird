# Codex Orchestrator 后续永久部署设计输入

## 结论与批准对象

本文记录当前 project-local 交付完成后，未来若选择永久部署需要另行设计和审查的范围，不执行任何操作，也不请求用户现在批准永久安装。当前 G4 的合同就是项目内 package、临时版本安装、`Doctor`、pin/unpin 和安全卸载；它不承诺生产 LaunchAgent、Codex plugin 注册或长期 native admission。后文六项是可选后续范围的先决条件，不是本轮 G4 的漏项。

未来若生产 admission 和部署方式均已实现并验收，届时的批准应绑定下列不可变值，不能只批准“最新版”：

- `FINAL_VERSION`
- `FINAL_PACKAGE_MANIFEST_SHA256`
- `FINAL_BINARY_SHA256`
- `FINAL_RUNTIME_MANIFEST_SHA256`
- `PINNED_PYTHON_PATH` 与 `PINNED_PYTHON_SHA256`
- `NATIVE_CODEX_PATH` 与 `NATIVE_CODEX_SHA256`

只有这些值已写入新的永久部署验收 receipt、后续先决条件已经实现并通过独立审查后，才应形成一份新的用户批准请求。当前没有这项请求。未来范围也不能默认包含登录、登出、账号切换、读取、复制或修改 Claude、AGY、Grok、Codex 或其他 CLI 的认证材料，或真实模型调用。

## 候选路径，不是已冻结产品合同

所有路径都属于当前 macOS 用户，创建的新目录必须为当前 UID 所有、真实目录且 mode 为 `0700`；现有目录不通过 `chmod` 接管。路径任一祖先为 symlink、所有者不符或权限过宽时停止。

| 用途 | 候选路径 |
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

安装根和 G1 数据根需要分离，因为当前安装器拒绝 `DestinationRoot` 与 `DataRoot` 相同或互相包含。表中的 G1 数据根沿用程序现有的 macOS Application Support 默认路径；其余永久路径尚未经过产品注册验证，不能据此创建目录。未来部署方案还必须证明卸载版本包不会删除该数据根。

## 当前 G4 已实现的项目内安装阶段

本轮最终报告可以确认的 G4 范围是：冻结 package、在临时 project-local 根执行版本 install/Doctor/pin/unpin/uninstall，以及验证 plugin 目录本身。它不能表述为生产 service 已激活、plugin 已永久注册或 native profile admission 已解决。

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

## 实验链已验证的原生服务配置形状

项目内 native fixture 已验证的入口形状是：

```text
PINNED_PYTHON_PATH -I -B \
  <固定版本根>/runtime/g0/projectproxy_launchd_entrypoint.py \
  --manifest <G1 用户数据根>/native/config/service-FINAL_VERSION.json
```

该入口只消费调度器提供的名为 `Listener` 的 socket FD；它不拥有或任意删除公共 socket 路径。下面的 manifest schema 已在临时 fixture 中验证，但不是可直接复制到生产的配置：

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

fixture 中的 `file_pins` 逐项列出 `runtime-manifest.json` 中的已安装绝对路径和 hash，不引用开发仓库。fixture isolation manifest 是另一份 `0600` JSON，配置类型为 `version:1` 加 `profiles` 映射；每个 profile 绑定 `task_root`、`home`、`codex_home`、`workspace`、`public_socket`、`backend_socket`、`expected_executable`/SHA-256、`protected_paths` 和 `protected_read_paths`。该结构会为新 backend 替换 `HOME`/`CODEX_HOME` 并清理环境，所以不能通过把路径从 `/private/tmp` 换到 Application Support 就成为生产方案。

生产 admission 必须先定义一份独立合同：每个连接都要从已验证的 owner/native handshake 得到 opaque profile ID、owner context digest、现有 backend 的 PID/birth/executable SHA-256、private socket identity、service manifest SHA-256 和连接 epoch，并把这些非秘密证明绑定到同一 lease。固定公共 socket 只能定位入口，不能用来猜测 profile 或 backend。admission 工具不得读取或持久化 credential 内容。

如果生产方案仍创建新 backend，就不能承诺它不会刷新、迁移或改写现有认证状态；这会超出当前无 auth 授权的边界。当前可继续评估的方向必须绑定并复用已存在、已证明属于该连接的 native profile/backend，或者另行提出明确的 auth 影响与用户授权。两者都尚未实现。

实验 G0 注册合同使用的 LaunchAgent plist 形状是：

```plist
Label = org.codex.orchestration.proxy
ProgramArguments = [上述固定 Python、-I、-B、已安装入口、--manifest、固定 manifest]
Sockets.Listener.SockPathName = /Users/guanjunhui/.codex/app-server-control/app-server-control.sock
Sockets.Listener.SockPathMode = 0600
```

未来若选择 LaunchAgent 部署，注册域应限定为 `gui/<当前 UID>`，并重新验证 plist 路径、mode、程序参数和 socket ownership。实验事务以 `0600` 写 plist 并记录文件身份和 SHA-256；生产事务尚未实现，不能直接调用 user LaunchAgent bootstrap。未来 activation receipt 至少需要 transaction ID、版本、package/runtime/service/plist SHA-256、program arguments、launchctl 返回值、job 身份、socket 身份和状态，且不能记录环境变量或认证内容。

## Codex plugin 部署候选

当前版本根是 G4 管理的不可变安装，不应手工改动。Marketplace 是一种待选部署方式，不是当前产品唯一入口。下面的 layout 和 entry 只符合 `plugin-creator` 的项目 marketplace 形状，尚未用安装后的版本根完成 Codex 产品注册、更新和卸载验证：

```text
marketplace/
  .agents/plugins/marketplace.json
  plugins/codex-orchestrator/
```

候选 marketplace entry 为：

```json
{
  "name": "codex-orchestrator",
  "source": {"source": "local", "path": "./plugins/codex-orchestrator"},
  "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
  "category": "Productivity"
}
```

这里的 `policy.authentication` 是 marketplace schema 的必填枚举，不等于允许 CLI 登录；plugin 自身仍为 `capabilities: []`。若最终选择 marketplace，必须先通过 `plugin-creator` 的 marketplace/cachebuster/reinstall 流程验证添加、升级和精确撤销，不能手工覆盖已有 marketplace 或全局 Codex 配置。投影内每个文件需要与 `<固定版本根>/manifest.json` 的对应条目相同，并单独记录 projection receipt；不能用未核验 symlink 指向活动版本。若选择其他受支持的 plugin 部署方式，也必须先冻结其实际产品入口和回滚合同。

无论选择哪种部署方式，plugin 被调用后仍应通过 `${PLUGIN_ROOT}/scripts/invoke.sh` 进入同一已安装 binary。`native-bridge start` 只在已有 G0 service/owner receipt 时建立回传 driver；它不是 launchd 注册入口，也不会代替原生 `codex` 启动命令。启动时保持静默，不自动 resume 历史任务。

## 后续可选范围的先决条件

以下项目不属于当前 project-local G4 合同。未来若决定做永久部署，任何一项未完成都应阻止该后续工作进入用户批准阶段：

1. **先冻结 production admission 合同。** 它必须按连接绑定已有 native profile/backend 的非秘密身份、lease 和 epoch，不能从固定 socket、进程名、工作目录或用户输入猜 profile，也不能复用临时 fixture 的 isolation context。
2. **选择并实现生产激活/停用入口。** 当前 CLI 只有 install/doctor/uninstall/pin/unpin 和 native bridge 命令；不存在可审查的 `activate`/`deactivate` 实现。`native-bridge start` 不能注册 LaunchAgent。
3. **选择服务生命周期实现。** 已审查的 `activation_transaction.py` 仍位于任务实验代码，`runtime/g0` 没有包含 production manifest builder、bootstrap/bootout controller 和恢复入口。LaunchAgent 只是候选，并非已决定方案。
4. **设计不改变 auth 语义的 profile/backend 绑定。** 当前 `auth_isolation.IsolationSpec` 强制新建 `/private/tmp/g0-auth-*` 根、替换 `HOME`/`CODEX_HOME` 并清理环境。简单改成持久目录会改变认证行为；新 backend 也可能刷新认证。生产方案必须先解决已有 backend 绑定，不能放宽这些校验来“永久化”。
5. **补齐服务级版本 pin 和恢复事务。** 现有 pin schema 绑定任务，不能证明长期 service manifest 仍引用某版本。未来需要 durable activation pin，并能在 manifest、service registration、pin 或 receipt 的任意崩溃窗安全 reconcile/deactivate。
6. **验证所选 plugin 部署方式。** 当前 G4 安装不创建 marketplace projection，也未验证其他产品注册入口。选择 marketplace 时需要验证 cachebuster、升级和精确撤销；选择其他方式时也要给出同等的版本绑定和回滚证据。

最小落地顺序是：先独立评审 production admission，证明每个连接如何绑定已有 native profile/backend 且不暴露秘密；再选择 service 与 plugin 部署方式；随后实现受限写集、service 级 pin、prepare/commit/reconcile/deactivate 和精确撤销；最后分别在临时环境和真实产品注册边界做可逆验收。不能先把 isolation root 改到持久目录，也不能先注册固定 socket 后再补 profile 证明。实现与最终 hash 冻结后，才能把本文占位符替换为真实值并另行准备用户批准材料。

## 停止、停用、卸载与记录保留

未来选定部署方式后，停用合同至少需要按以下顺序执行，并在任一步不确定时停止而不是扩大删除范围：

1. 停止接受新 submit；对该版本的任务逐项 `status`，显式 stop 活动 segment，并完成需要保留的 collect/ACK。`unknown`、开放问题、可恢复任务或 pending delivery 继续保留 task pin。
2. 由待新增的停用事务请求 native bridge driver 收口，确认 driver、helper 和已拥有的 backend 退出；现有公开 CLI 没有独立 driver stop 命令，因此实现前不能把发送任意信号当作这一步。不能只看 leader PID。
3. 如果选择 LaunchAgent，由同一 activation transaction 对 `gui/<UID>/org.codex.orchestration.proxy` 执行 bootout，并核对 job 消失、service/backend process identity 退出和固定 socket identity 被安全清理；其他部署方式必须提供等价的有界停止证明。
4. 写入最终 deactivate receipt 后释放 activation pin。任何 plist/socket/manifest 身份漂移都标记 `unknown` 并保留对象。
5. 如果选择 marketplace，使用 plugin 管理流程只撤销 `codex-orchestrator` 的 entry/cache，不重写其他条目，并保留撤销 receipt；其他部署方式按其已验证的精确撤销合同执行。
6. 对 `FINAL_VERSION` 先执行 `Doctor`，再调用现有 `uninstall --request`。安装器只删除 manifest 列出的未修改文件；changed、unknown、pinned 或 `cleanup_pending` 状态全部保留并报告。

默认保留 `/Users/guanjunhui/Library/Application Support/OpenAI/Codex Orchestrator` 下的 G1 数据库、control/owner receipt、native activation/deactivation receipt、delivery history 和 uninstall result。它们不由版本卸载器拥有。后续若要删除这些记录，需要另一个按 manifest 和 retention policy 审查的请求；本提案不授权删除。

## 最终用户看到的可审查包

激活实现完成后，提交给用户的批准材料应只有一组明确对象：

- 最终六项版本/hash/path 值。
- package manifest、安装 manifest/receipt 和 `Doctor` 输出。
- 所选 production admission、service 生命周期配置和 activation pin 的完整内容与 SHA-256；只有选择 LaunchAgent 时才包含 plist。
- 所选 plugin 部署配置与撤销证据；只有选择 marketplace 时才包含 entry、projection manifest 和 cachebuster 变更。
- prepare 阶段的拟写路径清单，以及 deactivate/uninstall 恢复步骤。
- 完整 native product 与资源验收的最终结果。

未来用户批准后才可写入已冻结的永久路径，并按已经选择和验证的方式注册 plugin/service。本文不提出该批准，也不授权 marketplace 注册或 user LaunchAgent bootstrap。未来批准也不得扩大到 auth、provider、模型、公开发布、许可证选择或 Windows 支持。
