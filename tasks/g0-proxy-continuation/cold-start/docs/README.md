# 冷启动 activation 前端

状态：**可运行离线入口与 fake-process 验证已实现；真实 launchd/native 尚未验收。** 用户已批准一次注册及对称撤销，auth 保护仍是执行硬边界，见 [安装与撤销](install-and-revoke.md)。

`projectproxy_launchd_entrypoint.py` 用 `launch_activate_socket("Listener")` 的真实 Darwin ABI 接收 listener，并把自有 duplicate FD 交给 proxy。原生 backend 不接受 activation FD，所以每个获准 frontend 都有独立 backend process 和 private UDS；没有共享 stdio。相同 profile 的连接串行使用其隔离 home，不同 profile 的 frontend 互不清理对方 backend。未登记 peer 不触发 backend；startup 空探针不消耗后续 WebSocket 连接的准入登记。

服务保留 PID/UID/birth/executable、连接生命周期、字节计数与 SHA256，丢弃 RPC 原文和进程 stdout/stderr 原文。每次 backend 启动前重验 isolation manifest、配置/执行文件 hash，以及 profile/state/grants 目录的 dev/ino/uid/mode。frontend EOF 停止本连接 backend；TERM/INT 请求关闭服务并等待自有清理。public socket 路径、权限和 inode 的生命周期属于 launchd，服务不会清理该路径。

[实现和测试收据](activation-implementation.md) 区分 fake C ABI + 真实 FD 与真实 launchd 激活；后者没有执行。测试只使用新短路径隔离 HOME 和 Python fixture，无真实 Codex、model、auth 或用户配置读写。已有通用 attachment 与完整 G0 不因此变为通过。

## 只读 renderer

`render_launchagent.py` 读取现有路径和文件，向 stdout 输出 JSON/plist，不写 LaunchAgents、不注册、不启动 backend。可指定 `--python ABSOLUTE_PYTHON --manifest ABSOLUTE_PRIVATE_MANIFEST --repo-root ABSOLUTE_REPO --home ABSOLUTE_USER_HOME`。它冻结解释器、入口和 manifest hash，并生成以下内部 argv：

```text
[absolute_python, "-I", "-B", absolute_entrypoint, "--manifest", absolute_manifest]
```

plist 固定 safe `PATH=/usr/bin:/bin`、`LANG=C`；entrypoint 清空继承环境，backend 环境来自 isolation guard。Listener 为默认 UDS、mode 0600，无 RunAtLoad/KeepAlive。解释器不依赖 GUI PATH 的 python3 解析。解释器/依赖/manifest 的正式准入仍由事务 preflight 做完整 hash 核对。

现有 socket/plist、目录不安全、入口缺失/不安全、manifest 缺失/不私有/endpoint 不一致时 renderer 拒绝。`installation_ready` 恒为 false，静态通过不是实际注册资格。[静态 XML](../data/org.codex.orchestration.proxy.plist) 只是路径模板，正式单次 case 必须用该 case 冻结 manifest 重新生成，并通过 `ActivationTransaction` 发布，不能直接安装模板。

## 剩余验收

主会话还需交叉核对 transaction、auth guard、service 和 pre-exec peer gate，再冻结实际 case。之后单次验证真实 GUI socket activation、普通无 prompt/model 的 Codex frontend、private native 的身份/生命周期、auth 前后保护值，以及精确撤销。所有 native/launchctl 实跑由主会话另行准入；当前代码实现不代替这些事实证据。
