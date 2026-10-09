# F01 validate 启动边界有限重试

仅支持已有 DAG 的 v1 candidate validate：`auto_directory=true`、source为认证producer或有序compose、固定原target/base、直接native工具入口。用户必须声明`command_sha256`，并显式配置有限`execution_policy.retry.codes`为`validation_start_eagain`/`validation_start_eintr`与有限backoff；沿用现有group/task/segment active预算和attempt上限。普通无retry validate保持旧兼容。

候选action示例（路径/base/pin须替换为当前实际值；该片段不是完整submit请求）：

```json
{
  "kind": "candidate",
  "directory": "/absolute/managed-validation-root",
  "candidate_action": {
    "version": 1,
    "dependency_input_version": 1,
    "operation": "validate",
    "source_task": "edit",
    "auto_directory": true,
    "command": ["/absolute/native-validator", "check"],
    "command_sha256": "<actual-lowercase-sha256>",
    "target": {
      "worktree": "/absolute/target-repository",
      "ref": "refs/heads/main",
      "base_oid": "<actual-base-oid>"
    }
  }
}
```

例如该task的预算与有限retry策略（须与既有submit task上限一致）：

```json
{
  "max_attempts": 3,
  "max_active_ms": 10000,
  "execution_policy": {
    "version": 1,
    "group_max_attempts": 3,
    "group_max_active_ms": 30000,
    "task_max_active_ms": 20000,
    "segment_max_active_ms": 10000,
    "retry": {
      "codes": ["validation_start_eagain", "validation_start_eintr"],
      "backoff_ms": [1000, 5000]
    }
  }
}
```

工具首次执行和每次retry都校验原路径/pin；Host流式复制到worker不可写的private sealed路径，绑定本次grant并执行固定copy。每份copy最高32MiB，claim前预约raw加8192metadata escrow，单版journal最高4096bytes。工具删除必须有原活Host未启动或原Handle Wait/tree终态证明及精确inode/hash/目录身份，durable removed journal后才退款raw；metadata保留。

该retry只处理整个sandbox wrapper的原`cmd.Start`返回EAGAIN/EINTR且Process/State为空、Context仍live的一次typed证明。此前Git物化已发生；sandbox成功后内部exec失败、已启动validate失败、UNKNOWN、权限/取消、任意implement/integrate失败均不自动retry。每次新attempt保留完整source字节/receipt/vector、adapter、tool pin和物理Host身份，使用新ID、新工具预约、新AutoDirectory worktree；预算不重置。

已有ACK/decision/claim/backoff/quota/hold约束继续有效；source/adapter/Host漂移、缺失或retired新decision binding直接拒绝。source快照读上限192KiB且完整decision沿用256KiB解析上限，容量或不可支持source仅ACK原认证launch_rejected、不建半队列；普通validate兼容。

冷恢复不会从JSON诊断或journal重建live never-started/Wait证明。原event已持久入Store可exact ACK replay；仅旧spool已持久但Store未记录，在新PID/birth后拒绝退款/排队，保留UNKNOWN/raw预约。本轮没有新增跨physical原publisher凭据。

验证使用本地受控native actor，无provider/model/account；原Start EAGAIN/EINTR为per-call受控故障注入，不是自然内核资源耗尽实证。工具仅固定入口binary，动态库/子命令依赖并非完全hermetic。实际sandbox七操作已配无sandbox正对照：self chmod/unlink、parent/ancestor chmod、peer chmod/write/unlink；未以self-overwrite的ETXTBSY/签名限制作为profile证明。未执行真实断电、Intel/Windows或Linux真实Git链；当前已安装产品/F13旧包不自动包含本功能。
