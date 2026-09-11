# 离线 G0 历史与 ACK 检查器

```sh
python3 tasks/g0-pending-resolution/scripts/delivery_audit.py \
  --input tasks/g0-pending-resolution/docs/delivery-audit-example.json
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s tasks/g0-pending-resolution/scripts -p delivery_audit_test.py
```

示例文件是明确的**合成测试输入**，不是 RPC 抓包。脚本只读这个显式文件；无网络、模型、数据库、目录发现或业务操作。原生 collector 的服务身份链与 ACK 记录来源必须独立审查；脚本不能验证它们。

输入顶层严格为 `version:1 / intent / reads / acks`。文件最多 64 KiB；最多 8 个事件、16 次读取、每页 64 项、32 条 ACK。重复 JSON key、非 JSON 表达式、NaN、超限、链接输入文件及不受支持形状返回固定错误与 exit 2，不回显载荷。正常输出一行有界 JSON、exit 0。

`intent` 同时就是期望出现在 standalone `output` 字符串内的完整 envelope：

```text
version=1
delivery_id / controller_thread_id / controller_epoch
events=[{event_id, event_revision, kind, payload_hash, action_slot}, ...]
payload_hash
```

本探针选择引用型事件：`events[].payload_hash` 是事先登记的固定结果/产物摘要，本工具不读取或验证产物正文。批次 `payload_hash` 是去掉最外层 `payload_hash` 后，对整个 envelope 按 UTF-8、`sort_keys=True`、`ensure_ascii=False`、分隔符 `,`/`:` 编码的 SHA-256。事件顺序参与摘要。标识限制为 1–64 个字母/数字/下划线/连字符；revision/epoch 为 1–1000000 的整数，布尔值不当整数；kind 是 progress/question/result。每事件必须预先声明唯一的 action_slot。

`reads[]` 保存原 `request:{id,method,params}` 与 `response:{id,result}` 的配对。支持整数 request id；失败 RPC、通知、`turn/start` 回执和 raw rollout 记录不在这次有限输入接口内。要采样失败/断线边界，应另外保存受限故障证据，不能伪装为成功历史读取。

- `thread/read` 必须 `includeTurns:true`，目标 thread id 匹配；读取 `result.thread.turns`。
- `thread/turns/list` 为完整匹配需 `itemsView:full`；保留 cursor/nextCursor/backwardsCursor 与排序参数。summary/notLoaded 只提示不完整，不提供标记阳性。
- `thread/items/list` 读取 `result.data[].{turnId,item}`。该方法能否用于实际 store，仍由原服务实验确定。
- 仅 `type:functionCallOutput`、`name:g0_delivery`、`namespace:orchestration` 的完整字符串 envelope 可匹配；其他类型从不转译。合法其他 delivery 的项可忽略，同 turn/item id 的内容冲突拒绝。

本脚本只解释有限读取投影，不是完整 app-server schema validator；无关 item 的正文不解析、不回显。`captured_page_chain_complete` 只描述给定读取页及 itemsView，**不证明发送窗口覆盖、并发落盘结束或 compact 数据完整**。有 compaction 项时单列 `compaction_seen`；不能从没有观察到该项推导未压缩。

`acks[]` 是已由外部采集器来源核验的逐事件记录输入，字段为：

```text
delivery_id / controller_thread_id / controller_epoch
event_id / event_revision / event_hash / action_slot
decision / command_id / decision_count / effect_count
```

每个字段必须匹配 intent 中的声明。decision 为 handled/waiting_user/stale/rejected；decision_count 固定 1，effect_count 对 handled 为 1、其他为 0。同事件重复记录即使换 command_id，也只占同一个输入槽；不同决定拒绝。这里的计数是对**传入记录**的核对，不认证本地数据库事务、不核验实际副作用，也不实现取消或修改 revision；这些仍属后续受控 G0 事务夹具。

输出将匹配历史、页链情况、重复展示与 pending/acknowledged/waiting_user 事件分开。恒定的 `source_service_verified:false`、`absence_proof:false`、`resend_authorized:false` 是范围声明：即使匹配精确标记，也不能单靠这个输出发送下一轮或判定整个 G0 通过。缺少 ACK 不会把匹配到的历史标记改成 uncertain；ACK 全部存在也不会把未匹配的历史改成已交付。
