# 安装、撤销与批准边界

本页只记录在实现和离线审查完成后可供用户执行的命令。当前不执行其中任何命令。

## 一次性用户级注册实验

这项注册绑定当前用户的 GUI launchd domain，并接管该用户普通 Codex 新连接使用的默认 UDS；它不是某一个 project 的私有服务，也不是系统级 daemon。影响范围是当前用户的 `gui/$(id -u)` domain、`~/Library/LaunchAgents/org.codex.orchestration.proxy.plist` 和 `~/.codex/app-server-control/app-server-control.sock`。不写 `/Library`，不要求 root，不修改账号凭据、Codex binary 或全局配置。

批准问题只有一个：**是否允许把这个用户级 LaunchAgent 注册到当前用户的 GUI launchd domain，以便按需接管该用户默认 Codex UDS？** 该批准若给出，同时覆盖这次实验完成后的对称撤销；撤销不另行索权。批准本身不能跳过下面的实现、静态预检和验收门槛。

在用户批准、`projectproxy` activationFD/runtime 接线完成、未实现项经离线审查、预检报告为 ready 且默认 socket 未被占用之后，建议由用户在 checkout 根目录执行：

```sh
PLIST="$HOME/Library/LaunchAgents/org.codex.orchestration.proxy.plist"
DOMAIN="gui/$(id -u)"

install -m 600 tasks/g0-proxy-continuation/cold-start/data/org.codex.orchestration.proxy.plist "$PLIST"
launchctl bootstrap "$DOMAIN" "$PLIST"
```

这两条命令的写入/注册范围分别是一个 mode `0600` 的用户 LaunchAgent 文件和一个当前用户 GUI domain 的 job。plist 没有 `RunAtLoad`、`KeepAlive`，所以注册不应在登录时自动启动业务任务；前端应当只在 activation 请求到达时拉起按需 backend。`bootstrap` 前仍需再次运行只读预检并确认 `blocking_reasons` 为空；预检发现已有 socket 或 plist 时必须停止，不得覆盖或接管。

## 对称撤销

实验完成、失败或需要回退时，在同一用户 GUI domain 执行：

```sh
PLIST="$HOME/Library/LaunchAgents/org.codex.orchestration.proxy.plist"
DOMAIN="gui/$(id -u)"

launchctl bootout "$DOMAIN" "$PLIST"
rm -- "$PLIST"
```

`bootout` 只卸载本次注册的用户 job；随后删除同一路径的 plist。若 job 已经产生 child，前端必须先按其归属合同停止自己的 child、保留记录并完成退出对账，再进行 `bootout`。不能用撤销命令代替业务停止，也不能删除不由本实验创建的 socket、plist 或其他进程。

## 验收门槛

安装前仍 blocked 的项目包括：activation FD 获取、projectproxy→private native 的逐连接转发、native runtime 接线、TUI/thread 附着与 epoch、最后前端退出后的停止/记录保留、多前端归属隔离以及新旧连接竞争。完成实现后，至少要在专用 checkout 中验证：

1. 冷连接命中公开默认 UDS，且真实 WebSocket handshake/initialize 在预算内完成；
2. backend 只绑定私有 UDS，公开入口不会被 native 的路径清理器删除；
3. 每个客户端连接拥有独立 native ConnectionId、请求/反向请求/订阅和退出边界；
4. 最后一个前端退出会停止本次后台执行并保留可对账记录，重新打开不会自动续跑，明确继续后才创建新 segment；
5. 两个前端互不认领对方执行，旧 transport epoch 和旧任务所有权会被拒绝；
6. 失败或完成后用上面的 `bootout` 回退，并留存命令、退出码、进程/套接字归属和 plist hash。

在这些证据齐全前，不得把这个候选描述成生产服务、默认已启用机制或完整 G0 通过。
