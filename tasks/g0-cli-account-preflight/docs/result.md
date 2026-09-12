# 私有 CLI 账户读取：当前账户 ID 一致，正常使用许可为 true

第二份有界私有服务已成功完成 `initialize`、`account/read(refreshToken:false)` 和 `account/rateLimits/read`。CLI 账户种类是 `chatgpt`；CLI 与桌面应用返回的 accountId SHA256 一致，双方 `ordinaryUsageAllowed=true`，同一 `codex` 周桶（10080 分钟）均为 usedPercent=11。CLI 读取于 08:44:36.175 UTC，桌面响应于 08:44:50.849 UTC；resetsAt 分别为 1789805388/1789805387，相差 1 秒。CLI 另有 `base_model_inference`、`codex_bengalfox` 桶，桌面响应未提供，缺失不算 0。[最终对比](account-comparison-02.json)、[CLI 脱敏结果](cli-account-read-02.json)、[桌面脱敏结果](app-usage-sanitized-02.json)

这确认的是当前账户和额度前提，不能复原 sd07 历史时点，也不能从本次读取认定当时 usage/weekly 文字属于缓存、失效提示或别的原因。没有消费 reset、登录或模型调用。

第二份严格复用已审 helper 的预连接 AF_UNIX socket 和 `compression=None/proxy=None/ping_interval=None` 等连接参数；升级前后服务身份一致。upgrade 1.13 ms，account/read 3.76 ms，rateLimits/read 822.99 ms，含清理总计 957.56 ms；每个记录阶段均小于 10 秒。服务 PID 63500 收到自有 process group 的 SIGTERM 后退出码 0，进程/group/socket 新鲜核验均已释放。[绑定](service-binding-02.json)、[收尾验证](verification-cleanup-02.json)

第一份失败与第二份阳性构成受支持连接方式的对照，但一次更换了整组已审参数，不能把根因单独归于 compression、proxy 或 preconnect。此后不再启动服务或 RPC。

## 第一份尝试的原始状态与边界

第一份任务自有的 0.154.0 app-server 使用私有 `tmp/a.sock`，未占默认 UDS。PID/birth、实际 executable path/SHA、socket owner/mode/inode 和服务持有该 socket 均已验证。WebSocket upgrade 随即报 `InvalidMessage`，因此没有发送 initialize、account/read 或 account/rateLimits/read；这一份结果的账户比较保持 unknown。[服务绑定](service-binding.json)、[第一份结果](cli-account-read.json)

同期桌面 `get_usage_limits` 成功：`ordinaryUsageAllowed=true`，`codex` 桶的 primary 为 10080 分钟、usedPercent=10，resetsAt=1789805388，secondary=null。此前准备阶段的独立桌面读取为 usedPercent=9；这些数值不是 sd07 历史时点的复原。原 accountId 仅在工具内存中参与哈希与比对，未输出或写入文件；没有 CLI 标识可以与它比较。[桌面脱敏投影](app-usage-sanitized.json)

固定版本确认的允许调用形状为：

- `initialize`：只有 `clientInfo.name/version`，随后标准 `initialized` notification。
- `account/read`：`{"refreshToken":false}`。
- `account/rateLimits/read`：`{"supportsLunaReserve":false,"excludeResetCreditDetails":true}`，避免 Luna Reserve 实验能力声明及额外 reset-credit detail 请求。

上述白名单已由第二份服务执行；第一份未发送。`account/read` 的 false 避免主动刷新；额度 handler 内部仍会调用 `AuthManager.auth()`，可能维护已有登录态。按本轮澄清，正常内部续期不是额外刷新操作；本次没有读取或解码凭据来判断它，也没有发送刷新、登录、消费或 reset 请求。[固定 schema](schema-receipt.json)、[account handler](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/account_processor.rs#L1129)

第一份失败仅定位到 WebSocket upgrade，不从异常类型推定认证、网络或额度问题。第二份在第一份退出/清理已核验后，按同一只读诊断目标使用新的私有目录运行；两者未并存。

第一份原生操作总耗时 126,544,417 ns，cleanup 耗时 13,713,833 ns；向已再次核验身份的自有 process group 发送 SIGTERM 后服务退出码为 0，私有 socket 已不存在。两份均未创建 TUI/thread/turn、未调用模型、未直接修改旧 case/配置/账号/CLI 链接。该结果仅是账户读取诊断，不计作 G0 资格或回传结果。
