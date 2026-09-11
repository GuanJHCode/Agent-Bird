# launchd socket activation：只读机制结论

**机制上可保留“普通 `codex` 先连接一个已监听的默认 UDS”这条候选；当前原生 binary 不能直接接管 launchd 的 listener FD。** 若增加前端进程，应研究每条真实客户端连接一对一转发到同一个原生服务的私有 UDS。多个客户端合并到一条 native stdio 连接不能视作透明转发。本页没有运行 launchctl、安装 plist、修改配置/套接字或启动模型；没有据此认定合同允许代理。

## 普通入口与冷启动预算

固定 `0.153.4` 源码的 `AUTO_CONNECT_DAEMON_CONNECT_TIMEOUT` 是 50ms；`maybe_probe_default_daemon_socket()` 仅执行 `UnixStream::connect`，成功即返回路径，探测连接随即释放。它不发送 WebSocket 握手或 initialize。源码测试只 bind listener、不 accept，也能通过这个探测。[常量与函数](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L447)、[socket-only 测试](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L2562)。

探测出错/超时，或启动参数不满足可重放条件时，target 选择 Embedded；命中路径时选择 LocalDaemon。这个决定在真实连接前完成。选定 LocalDaemon 后，`start_app_server` 返回远程连接结果，所核查的该连接分支没有失败后改建 Embedded 的逻辑。[探测与选择调用](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/startup_orchestration.rs#L178)、[target/可复用参数](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L860)、[实际连接分支](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L489)。

随后会另建 UDS 连接。该端点实际运行 **WebSocket-over-UDS**，不是裸 JSONL：UDS connect 和 WebSocket upgrade 各有独立 10s 上限；initialize 回应还有独立 10s 上限。[超时常量](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-client/src/remote.rs#L64)、[UDS 与 upgrade](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-client/src/remote.rs#L748)、[initialize 等待](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-client/src/remote.rs#L823)。

据此推导：launchd 预先 listen 的队列可能让 50ms connect 无需等待服务进程初始化；冷启动成本主要落在后续握手预算。但这不是延迟保证。监听路径/权限/积压或调度异常仍可能使初始探测失败；服务若在探测成功后启动过慢，则真实握手可能失败。没有安装和采样，不能声称这个候选已解决启动竞争。

## 原生 listener 不接受 activation FD

公开 `--listen` 只解析 `stdio://`、`unix://`/`unix://PATH`、`ws://IP:PORT` 和 `off`。Unix 分支依次调用 `prepare_control_socket_path`、`UnixListener::bind`，底层在 Unix 上直接使用 Tokio 的 bind。已核查的入口没有 inherited-listener 参数、`LISTEN_FDS`/`LISTEN_PID` 或 `launch_activate_socket` 接管分支。[transport 类型/解析](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/mod.rs#L74)、[创建 listener](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/unix_socket.rs#L24)、[Unix bind](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/uds/src/lib.rs#L140)。

把 native `--listen unix://` 直接指向 launchd 已经监听的同一路径不会复用 FD：预检查 connect 成功后直接返回 `AddrInUse`。原生 listener 自己正常退出还会通过 `ControlSocketFileGuard` 删除其绑定路径。因此 activation 前端路径和 native 私有 backend 路径必须分开，不能让原生路径清理器接管 launchd 的公开入口。[冲突检查](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/unix_socket.rs#L93)、[退出清理](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/unix_socket.rs#L178)。

## 多连接方案的准确范围

native stdio 只创建一个 ConnectionId，服务标记为 single-client。把多个 TUI/bridge 的 JSON-RPC 合流，会折叠 initialize 状态、请求 ID、服务器反向请求/审批路由、订阅及断线作用域；额外做一层协议虚拟化也不能直接称为透明。[stdio 单连接](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/stdio.rs#L25)、[single-client 模式](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/lib.rs#L719)。

较小的机制候选是：一个 activation 前端取得监听 FD；所有客户端都转到**同一个** native 私有 UDS 服务；每条前端连接对应一条独立后端连接，并端到端转发 WebSocket 握手与字节。原生 Unix accept 循环为各连接运行处理器，后者各分配 ConnectionId，因此可以保留原生多客户端作用域。[accept/upgrade 循环](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/unix_socket.rs#L46)、[每连接分配 ID](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-transport/src/transport/websocket.rs#L181)。

这是至少“一个前端 + 一个 native 服务”的进程组合，不能称为仅一个总进程。frontend 还需避免并发连接各自启动一个 backend。字节转发不证明原始客户端的 OS peer 身份被保留，也不提供当前 TUI→thread 正证据。backend 退出会使原连接/内存请求失效，前端不能伪造原生响应或把旧 transport epoch 当成新服务的有效归属。

## launchd 的生命周期与等待

本机 `launchd.plist(5)` 的 `Sockets` 定义按需监听，要求服务通过 `launch_activate_socket(3)` 取回 FD；`SockPathName` 表示 Unix 路径，`SockPassive` 默认 true 即 listen。`launch_activate_socket` 返回该 socket 名对应的 FD 数组，服务负责接受连接。未被 launchd 管理、名称不存在或重复 activation 分别可能得到 ESRCH、ENOENT、EALREADY。[本机 man 文件/hash](idle-socket-activation-sources.json)、[Apple API](https://developer.apple.com/documentation/xpc/launch_activate_socket)。

Apple 的按需机制是在 job 不运行时持有其资源；已加载 job 退出后可在未来请求到达时再次启动。普通非 inetd 模式允许服务进程自己 accept 多连接；`inetdCompatibility` 会改变 FD/每连接进程语义，不适合拿来直接包装原生 stdio。服务退出不等于卸载 job；前端 UDS 的持续可用只在 job 仍加载、对应用户会话/路径仍有效等条件下成立。native child 单独退出时，应由前端处理 child 生命周期；launchd 只管理所注册的 job。[Apple 按需服务说明](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)。

本机 `ThrottleInterval` 默认策略限制频繁拉起为约每 10 秒一次；因而不能承诺 crash 后立即重启，也不能用缩短节流掩盖冷启动预算。服务可以等待授权或活动连接，但授权仍由原生协议处理，activation 本身不批准工具或转移账号权限。此处未验证具体多客户端审批样本。

## 与现有合同的交界

旧实验已经证明：有现存 listener 时普通 `codex` 隐式复用；没有时会落入 in-process。新建服务不会迁移一个已经运行的 in-process TUI。另一个关键差异是：共享服务不会随一个 TUI 的 `/quit` 退出，先前“原生直接父消失”守卫不能在此拓扑下直接代表“TUI 退出就停止执行”。两类事实不能相互替代。[既有 daemon 实测](../../../docs/implementation/g0/daemon/result.md)。

这条候选增加用户级 LaunchAgent 与 TUI 必经前端，超出当前已批准的 Skill/Hook 集成实现范围；“命令仍叫 codex”不足以说明合同不变。只有主会话完成合同判断，并解决当前附着及其失效、设置连续性、停止执行和对账门槛后，才有下一步实验资格。本轮只读机制结果不授权安装。

来源记录包括两个固定 native 版本的 27 个定点文件及三份本机 Apple man 的 hash。旧版 `tui/src/app_server_connection.rs` 下载曾两次 exit 56；没有把下载失败当作版本差异，而是用成功的固定 ref 目录查询确认该文件名不存在、`app_server_session.rs` 存在后改读实际模块。完整命令、退出码、目录响应 hash 和当前缺失状态已保留。[来源与失败处理](idle-socket-activation-sources.json)。
