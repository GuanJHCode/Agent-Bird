# 旁路观察器输入契约

`tasks/g0-tui-proxy/scripts/proxy_observer.py` 接收透明 relay 提供的内存字节副本，不参与转发，也不保存 raw transcript。调用方按连接生命周期调用：

```python
epoch = observer.open(conn_id, conn_epoch=optional_epoch)
observer.feed(conn_id, "client" | "server", bytes_chunk)
observer.close(conn_id)
events = observer.drain_events()
```

`feed` 可接收任意 chunk 边界。观察器先解析 HTTP Upgrade，再按方向校验 WebSocket mask（client 必须 masked，server 必须 unmasked）、分片、控制帧和长度上限。当前观察器不实现 WebSocket 扩展解码，非空 `Sec-WebSocket-Extensions`、RSV 位和其它不支持扩展都会生成 `gap(reason="unsupported_extension")` 或对应 gap；这不是对 native 0.154 HTTP 协商行为的推断。固定源码中的 `InitializeCapabilities.extensions` 属于应用层 initialize capability，不能当作 WebSocket HTTP 扩展协商证据。若原生版本启用扩展，应先补充独立握手证据并更新解析策略。

所有输出事件带 `conn_epoch`（无连接上下文的未知连接 gap 除外）和单调 `local_seq`。RPC 白名单输出仅保留方法、方向、请求 id、`threadId`/线程元数据以及 `skills/list` 的 `cwds`、`forceReload`。响应只输出 `ok` 和有限线程元数据；error/result 正文、用户 text、instructions、env、auth、历史列表和 raw 字节都丢弃。未知方法、未知通知、孤儿响应和 `thread/list` 响应输出 `rpc_unknown`（含 method/id/方向和 `opaque=true`）作为邻接屏障，不把它们静默跳过。

重复 JSON key、非法 JSON/UTF-8、错误 mask、非法 frame、超限、EOF 中断和未知连接都会发出 `gap`。gap 将该 epoch 置为 `PENDING`；`close` 最终置为 `DISCONNECTED`。观察器不把任何 read、skills 刷新或最近请求推断为当前显示线程，复杂附着判定留给独立 source/动作核验。
