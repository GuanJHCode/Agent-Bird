# sd03 线程关系字段

本机固定 `b973…1261e3 / 0.153.4` 生成的 [schema receipt](schema-receipt.json) 与既有 `ThreadReadResponse` 摘要一致。只生成并读取 schema，没有启动服务或模型。

JSON Schema 将 `parentThreadId` 和 `forkedFromId` 声明为 nullable、non-required；这不能单独证明实际响应会省略空值。固定 [Rust Thread 定义](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs#L200) derive `Serialize`，两个 `Option<String>` 没有 `skip_serializing_if`，因此 `None` 正常序列化为 `null`。sd03 仍显式要求两个字段出现且为 null；真实缺字段时拒绝，不能推断无父关系。

`sessionId` 是同一 session tree 共享的 UUID，要求前后一致，不假定等于 root。`source=vscode` 也不单独证明主控角色；固定资格同时检查本地 spawn edge 与真实 Go owner。

独立 reviewer 已删除完整 schema 缓存。主会话核对 receipt 字节后归档并删除其临时副本，见 [归档清单](archive-manifest.json)。
