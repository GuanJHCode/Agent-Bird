# 受控原生 resume 资格（离线实现）

本轮只准备 resume 的协议资格与受控入口，未启动 native、模型或账号操作。

`ResumeAttachmentInferer` 独立于旧的 `AttachmentInferer`。它从空白连接 epoch 开始，只接受以下连续证据：客户端和服务端 WebSocket upgrade、成功的 `initialize`、客户端 `initialized`、客户端 `thread/resume` 且安全参数 `threadId` 等于明确的 canonical UUID、同 request id 的服务端成功响应，响应中的 `thread.id` 与 UUID 相同，以及该响应之后新发出的 `skills/list` 和对应成功响应。候选身份只取自 `thread/resume` response；请求 UUID、`/status`、可选的 `thread/started` 都不能单独形成身份。若 `thread/started` 带 ID，则必须与 UUID 相同。

EOF、close、gap 会使当前 epoch 失效。失效前出现新 epoch 会拒绝，失效后新 epoch 不继承旧候选。bootstrap 请求/响应要求方向、request id 和方法严格匹配；提前的 skills 流量不会成为 resume 后的 barrier。Bridge 仅将 Observer 已经安全提取的 resume `threadId` 写入 trace，不保存 raw params 或历史正文。

`NativeRuntimeConfig.resume_thread_id`（也可用等价的 `expected_resume_thread_id`）是显式受控入口。它生成完整 argv `(cli, "resume", canonical_uuid)`，没有 prompt、model、remote 或 config override。未指定时仍只生成 `(cli,)`。`tasks/g0-proxy-continuation/scripts/resume_case.py` 只创建 O_EXCL 的 preflight 目录/记录，记录绝对 argv、binary/config hash、前后端 endpoint 保护和原 10/120 秒窗口；它还提供独立 status、silent turn/start 检查以及 quit/EOF/owned-cleanup 的结果校验和 O_EXCL result 写入，但没有 native 执行路径，必须由主会话审查后才能进入实际验证。

当前验证：旧 proxy 测试 134 项全绿，continuation resume/harness 测试 13 项全绿。真实 native 和模型验证仍未执行。
