# 冷启动 LaunchAgent 草案

状态：**仅供审查，当前阻塞，不可安装**。

本目录把已确认的 launchd socket activation 机制落成一个可检查的用户级草案。机制证据见 [`idle-socket-activation-mechanism.md`](../../../g0-pending-resolution/docs/idle-socket-activation-mechanism.md) 和 [`idle-socket-activation-sources.json`](../../../g0-pending-resolution/docs/idle-socket-activation-sources.json)。原生服务不接受 inherited activation FD，因此目标拓扑必须是 `projectproxy` 前端取得 activation FD，再把每一条客户端 WebSocket-over-UDS 连接一对一转发到 native 的私有 UDS；不能把多个客户端合并到 native stdio。

## 草案内容

- Label：`org.codex.orchestration.proxy`
- 安装草案路径：`~/Library/LaunchAgents/org.codex.orchestration.proxy.plist`
- launchd 按需监听：`~/.codex/app-server-control/app-server-control.sock`，请求的 `SockPathMode` 是 `0600`
- `ProgramArguments` 固定指向主仓稳定路径 `tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py`
- plist 不含 `RunAtLoad` 或 `KeepAlive`。业务任务不能随登录自动续跑；打开会话也不能自动恢复任务。

可审查的 XML 草案是 [`org.codex.orchestration.proxy.plist`](../data/org.codex.orchestration.proxy.plist)。其中引用的主仓稳定路径启动脚本尚不存在，这是刻意保留的阻塞信号，不是可安装成品。不能用占位脚本掩盖 activation FD、private native runtime、停止和归属语义尚未实现的事实。

## 只读预检与渲染

[`render_launchagent.py`](../scripts/render_launchagent.py) 只读取 `stat` 元数据和启动脚本字节，向 stdout 输出 JSON 报告；它不会创建目录或 plist，不会写 `~/Library/LaunchAgents`，不会执行 `launchctl`，也不会启动 proxy/native。报告包含：

- 安装路径、默认 socket 路径和绝对启动脚本路径；
- socket 已存在时的路径冲突，以及已有 socket 的 mode；
- socket 父目录（要求当前用户所有且 owner-only）和 `LaunchAgents` 目录（拒绝 group/world writable）的权限；
- 启动脚本是否为普通文件、owner 是否可执行、group/other 是否可写及 SHA-256；
- 由同一份绝对路径生成的 plist XML 和 `/usr/bin/plutil -lint -` 静态校验结果。

在主仓根目录运行以下命令只会产生 stdout 报告；退出码 `0` 代表静态条件满足，退出码 `2` 代表阻塞。报告的 `status` 即使为 `preflight_only` 也不表示安装就绪；`installation_ready` 恒为 `false`，均不表示已经注册服务：

```sh
python3 tasks/g0-proxy-continuation/cold-start/scripts/render_launchagent.py --repo-root "$PWD" --home "$HOME"
```

已有默认 socket、安装路径已存在、启动脚本缺失/不可执行/可被 group 或 other 写入、目录缺失/不是实际目录/是 symlink、权限不合要求或 `plutil` 校验失败时，报告会列出 `blocking_reasons`。预检拒绝覆盖现有 socket 和 plist。

## 当前明确阻塞

1. `projectproxy` 尚未实现：没有从 launchd 取得 activation FD、接受客户端、保持每客户端独立 backend 连接并转发 WebSocket 握手/字节的代码。
2. native 继承 socket 的 runtime 接线尚未实现。native 必须绑定私有 backend UDS，不能直接把 `--listen unix://` 指向 launchd 公开入口。
3. 当前没有证明 TUI 到 thread 的附着、transport epoch 失效、配置连续性、权限/问题请求路由和多前端归属隔离。
4. 最后一个前端退出后的停止边界尚未实现和验收。目标行为是停止该用户本次后台执行、保留记录和可对账进度；不因 LaunchAgent 仍加载而继续执行，也不在重新打开时自动恢复。
5. 多前端必须按前端/用户会话建立归属隔离；不能让一个前端退出清理另一个前端的执行或复用旧 transport epoch。

因此，本草案不能证明冷启动候选已生产可用，也不能替代 G0 的附着、退出停止和交付对账验收。
