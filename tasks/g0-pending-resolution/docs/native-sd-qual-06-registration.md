# sd-qual-06 离线冻结

唯一 active nonce：`sd-qual-06`。paste 前须在初始 120 秒窗口内观察到本 service 唯一 `SessionStart(source=startup)`；该窗口从 origin 捕获后开始，覆盖 SessionStart 等待与随后仅一次 paste+Enter。缺失失败码 `owned_session_start_missing`，并 O_EXCL/0600 归档有界 PTY 字节（JSON 仅 path/bytes/sha）。未改 MCP、全局配置、账号权限；未启动 native/model；sd02/sd03/sd04/sd05 原件不改。四份未参与实现的交叉复核有效问题 0。
