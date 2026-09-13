# native-tui-02 空探活根因

原 MAIN 4de0fad case 保持 UNKNOWN、不改记录。`process identity unavailable` 来自 TuiSession.ready → check_connected_backend → peer_for_process(45527)。唯一 owner-connected-397bf39d984d 对应的 backend-final 已 closed/process_stopped/socket_removed；唯一连接只有 connect/EOF/disconnect，无字节流、无 RPC、无 thread。它是普通 TUI 的默认 socket 空探活创建并关闭的 backend，不是活 WS；控制器误把其历史 connected receipt 当成活 backend。因此不能用身份重试或忽略历史 lease 解决。

只读后验：TUI45500、service45489、backend45527、guardian45524 均已 gone，record.private_socket 已 absent。root 的 private_sockets_absent=false 表示该失败case未取得预期两份 closed_backends 证据，并不表示实际残留，也不归因清理时序。作者未发信号、未运行 native/launchctl、未读写真实auth或原case。

修复：ProxyServer 增加默认关闭的 wait_for_frontend_data，ActivationService 显式开启。peer admission 后先在原 callback 10秒截止前读取一个 bounded chunk；空 EOF 直接结束、0 backend、0 authenticated connection epoch。非空 chunk 保存在局部内存，创建 backend 的 wait_for 使用同一截止余量；随后作为 _pump 的首段依原顺序 capture/gate/forward，既不丢失也不通过 feed_data 追加乱序。普通非activation proxy 默认行为不变。empty_probe_eofs 仅为获准空探活的安全计数。

真实独立 dummy TUI 现在先空探活再 WS，已通过跨service resume/分页/实际Go ACK组合，每个service仍只有一个实际backend。另一个实际UDS测试用chunk_size=1检查首段+余下payload的capture与回传逐字节一致。旧activation原始身份fixture改为client-first测试握手，保留同profile串行及grant复用约束；空探活期不再期望创建第三个backend。

RED 在原代码的同路径观察到被销毁探活socket不可用。最后生产修改后：34 passed /17.66s，6条原fork DeprecationWarning保留。仅运行 test_activation_probe_transport.py、test_native_tui_case.py、test_proxy_transport.py、test_activation_service.py；安全Python/pytest环境沿 native-tui-combined.md。证据 native-probe-eof-{red,green}.log。不扩大原窗口、不重新发送、不迁移owner或降低身份门禁。
