# 旁路观察器输入契约

`tasks/g0-tui-proxy/scripts/proxy_observer.py` 接收透明 relay 提供的内存字节副本，不参与转发，也不保存 raw transcript。调用方按连接生命周期调用：

```python
epoch = observer.open(conn_id, conn_epoch=optional_epoch)
observer.feed(conn_id, "client" | "server", bytes_chunk)
observer.close(conn_id)
events = observer.drain_events()
```

`feed` 可接收任意 chunk 边界。观察器先解析 HTTP Upgrade，再按方向校验 WebSocket mask（client 必须 masked，server 必须 unmasked）、分片、控制帧和长度上限。当前观察器不实现 WebSocket 扩展解码，非空 `Sec-WebSocket-Extensions`、RSV 位和其它不支持扩展都会生成 `gap(reason="unsupported_extension")` 或对应 gap；这不是对 native 0.154 HTTP 协商行为的推断。固定源码中的 `InitializeCapabilities.extensions` 属于应用层 initialize capability，不能当作 WebSocket HTTP 扩展协商证据。若原生版本启用扩展，应先补充独立握手证据并更新解析策略。

## 0.154 JSON-RPC wire 形状

固定源码 `tasks/g0-native-0154-contract/data/source/codex-rs/app-server-protocol/src/rpc.rs` 的第 1–2 行明确说明 Codex 不发送也不期待 `"jsonrpc": "2.0"`；第 34–42 行用 untagged enum 区分 request、notification、response、error。第 44–88 行定义真实字段：request 必须有 `id`、`method`，notification 必须有 `method` 且没有 `id`，response 必须有 `id`、`result`，error 必须有 `id`、`error`，其中 error 必须包含整数 `code` 和字符串 `message`。因此观察器按 `method`/`result`/`error` 判别 envelope，缺少这些必需字段或类型不符时 fail-closed；缺少 `jsonrpc` 是有效的 native wire。为兼容既有样本，显式出现的 `jsonrpc` 只接受字符串 `"2.0"`，其它版本、类型或 `null` 仍生成 `gap(reason="invalid_json_rpc")`。

## WebSocket 容量与超限诊断

固定 0.154 的 `app-server-transport` 使用 `axum 0.8.8`、`tokio-tungstenite 0.28.0` 和 OpenAI fork 的 `tungstenite 0.27.0`；版本 receipt 为 `tasks/g0-attachment-log-availability/docs/source-receipt.json`，固定源码 commit 为 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`。本地固定源码 `tasks/g0-attachment-log-availability/data/source/codex-rs/app-server-transport/src/transport/websocket.rs:105-125,289-388` 的 Upgrade 调用没有覆盖 `.max_frame_size()` 或 `.max_message_size()`；底层默认允许单个 frame payload 16 MiB、完整 message 64 MiB。native outbound 将完整 JSON 序列化为一个 `Text` message，inbound 将完整 `Text(String)` 交给 `serde_json::from_str`，因此插件目录响应不能假定会被 native 自动拆成小 frame。`plugin/list` 的固定协议返回 marketplace/plugin metadata 数组，协议没有插件数量上限。

observer 默认仍是单 frame 1 MiB、完整 message 2 MiB；runtime 若按 native 对齐显式传入 16 MiB/64 MiB，默认值不会改变。所有超限仍 fail-closed：`frame_too_large` gap 只增加整数 `payload_bytes` 和 `limit_bytes`，`message_too_large` gap 只增加整数 `message_bytes` 和 `limit_bytes`，不保留或输出 payload/raw。长度来自 WebSocket frame header 或已累计的分片计数。

容量提升仍是有界的：输入 buffer 上限为 `max_message_bytes + max_header_bytes + 32`，分片 payload 不超过 `max_message_bytes`；默认配置下每个方向约 2.06 MiB，16/64 MiB runtime 配置下每个方向约 64 MiB 加 header。JSON 仅在完整 message 已通过该上限后解析；递归过深由 `json.loads` 的 `RecursionError` 转为 `invalid_json`，不会输出正文。长度 metadata 用于诊断超限位置，不改变未知方法 opaque、重复 key、类型/id 校验或 gap 状态。

Upgrade 还严格校验 HTTP/1.1、WebSocket version `13`、16-byte canonical base64 key、精确 header token，以及基于 ASCII key 和 RFC GUID 计算的 `Sec-WebSocket-Accept`。pending request、`skills/list` 的 `cwds` 项数和整数 request id 均有固定上限，超限会 gap 并使 epoch 失效，不会静默截断。

所有输出事件带 `conn_epoch`（无连接上下文的未知连接 gap 除外）和单调 `local_seq`。RPC 白名单输出仅保留方法、方向、请求 id、`threadId`/线程元数据以及 `skills/list` 的 `cwds`、`forceReload`。响应只输出 `ok` 和有限线程元数据；error/result 正文、用户 text、instructions、env、auth、历史列表和 raw 字节都丢弃。未知方法、未知通知、孤儿响应和 `thread/list` 响应输出 `rpc_unknown`（含 method/id/方向和 `opaque=true`）作为邻接屏障，不把它们静默跳过。

重复 JSON key、非法 JSON/UTF-8、错误 mask、非法 frame、超限、EOF 中断和未知连接都会发出 `gap`。gap 将该 epoch 置为 `PENDING`；`close` 最终置为 `DISCONNECTED`。观察器不把任何 read、skills 刷新或最近请求推断为当前显示线程，复杂附着判定留给独立 source/动作核验。
