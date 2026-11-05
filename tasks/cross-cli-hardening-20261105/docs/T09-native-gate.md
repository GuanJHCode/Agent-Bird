# T09：原生回调 gate 与回退

本轮不新增原生通道；native 验收 BLOCKED，T09 不标 done。沿用已审查的 T07 managed_process gate 和 T08 普通 MCP 工具：native_push=false，wait/collect 可用；非 collect 的 managed 提交在写入前返回 managed_owner_native_delivery_unavailable。成功能力响应没有 reason 字段，该原因来自拒绝错误码及本说明，未虚构新增运行时字段。

PID/birth/ancestry 只证明本机进程归属，不证明进程内 chat/thread 的回调路由。当前不自动认领或接管已打开的 TUI，不根据通知、回执历史或工具装载推断父模型续审。原 legacy 受控 App Server/helper 通道保留其原合同与范围，不因该 gate 改写授权。

固定 Go SDK1.8.0 的普通 MCP 实现没有 Tasks API。Codex0.166.0 本地导出 schema 有 turn/start.toolOutput；这只证明字段存在。真实 metadata 加载8工具仅证明工具装载，未创建 thread/turn、未调用模型。Claude2.1.316 的 owned-loopback worker 工具验证也不证明父会话逐请求 Tasks。上述证据均不能解锁 native。

PUSH-01：当前同包/同 pin 的 idle、busy 原 thread 投递和真父模型续审未验。PUSH-02：Claude逐请求 Tasks、同 revision 等待和父续审未实现/未验。PUSH-03：clear/resume、断连、EOF、旧 revision 的真实宿主语义未验。各项保持 BLOCKED，不把 collect live 闭环归为 native 回调。

PUSH-04 的非 live 机制由现有 coordinator managed gate 与 T08 cancel/EOF/ACK/accept/collect 测试覆盖，最终稳定树报告再绑定；该文档自身不产生新的测试 PASS。T09 只读调查和精确源码/schema/SDK SHA 位于本机任务外部 tmp/t09-research，未读取认证密钥、未调用模型或改全局配置。未来 native enable 需要另外的协议实现和目标宿主真实证据，不能仅比较版本号。
