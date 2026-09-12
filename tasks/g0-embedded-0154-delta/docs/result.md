# embedded 入口的 0.154 有限版本续验

**本次小差异没有新增 embedded runtime 的外部接入能力；此前该具体启动链的否定结论可续验到 0.154.0。** 这不是对全部隐藏能力或其他调用链的否定。

两份 `app-server/src/in_process.rs` 从已保存的 fixed manifest 原 URL 获取，并严格匹配原字节数和 SHA256：

- 0.153.4 / `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a`：45627 bytes，`5dd5f615be26b7fc961c73ac42ebaeb24871eb895aaa4b3035d4cdafd054ebf8`。
- 0.154.0 / `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`：45705 bytes，`7abb2569238e738caff88235a6f884a1f2b60a8dd7074c56dbb2e2fc2ee0d1b5`。

完整差异只有三处内部 `notify_client_response/error` 调用补传既有常量 `IN_PROCESS_CONNECTION_ID = ConnectionId(0)`，每处增加 26 bytes，合计 78 bytes；反向归一化后两文件逐字节相等。[完整 diff](in-process.diff)

当前调用链也按原收据复核：`tui/src/lib.rs:612` 构造 `InProcessClientStartArgs`；`app-server-client/src/lib.rs:238` 转为 `InProcessStartArgs`，`:327` 调用 `in_process::start`。这些参数没有 listener/endpoint 或可供 Hook 开启该 runtime 外部通道的参数。`in_process.rs:425` 仍创建内存 mpsc channels，`:444` 只登记内部连接 0，`:488` 仍设 `rpc_transport: InProcess`，`:489` 仍设 `remote_control_handle: None`。公开 Rust `sender()` 仍是进程内句柄，不构成外部 bridge 端点。

本次只续验上述文件差异和当前 TUI→client→runtime 启动链；未重复 async exec、child 或 Hook 触发链。未执行 native 命令、连接服务、发 RPC、修改运行源码或配置，main HEAD 仍为 `6c0a80531140b004b200a347b026d315499a6783`。保留两份源码、diff 与 [校验收据](verification.json)，无临时文件待清理。
