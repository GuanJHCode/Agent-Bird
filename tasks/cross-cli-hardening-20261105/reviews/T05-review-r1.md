# T05 独立审查：CHANGES_REQUESTED

审查者 /root/review_t05；实现者 /root/implement_t05。未参与实现，未修改源码，未调用真实模型。冻结树 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05`，HEAD `428a5877d63247d0dd0b345b0765aa3c912507c2`，实际9文件全部与 `source-manifest.json` SHA256一致。已审阅实际 diff、新增 `test_delivery_deadlines.py` 及消费者 `native_product_case.py`。

## P1：部分 WebSocket frame 读取超时后复用已失步连接，回执可恢复等待被误报失败

位置：`runtime/native/owner_helper.py:657` 的 `read_rpc()`，`runtime/native/delivery_adapter.py:470` 响应等待，`plugins/codex-orchestrator/scripts/native-product-bridge.py:338-353,934-940,1044-1058`。

触发：turn/start 已进入 durable sending；响应 frame header 已到达，payload 或后续 fragment 尚未完成时 response/read deadline 到期。`asyncio.wait_for` 取消 `_read_server_text`，此前 header bytes 已被消费，但连接 `_closed` 仍为 false。`observe_delivery` 捕获 transport异常并标 uncertain，随后在同一 connection 上 `exact_history`。迟到 payload 会被解释为下一 frame header。实测得到 `websocket_compression_or_reserved`。这不是错误 proof，而是本地丢失 parser边界；standalone异常分类未将其视为恢复等待，写 `status=failed`，helper同样抛出。

复现命令：

```sh
PYTHONDONTWRITEBYTECODE=1 /private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python /Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05-evidence/review-t05/scripts/reproduce_partial_frame.py /Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05
```

精确输出：

```text
first_read=TimeoutError after frame header consumed
connection_closed= False pending= {2}
second_read= OwnerHelperAdmissionError websocket_compression_or_reserved
```

最小修复：一旦 frame read被超时/取消中断，不能复用当前 parser连接；有界关闭/废弃连接，保持原 delivery ID/hash/owner/epoch 与 uncertain状态，以 history-only新连接恢复，或先返回 recoverable awaiting_history等显式恢复。不能靠把所有协议校验错误降级来隐藏实际错proof。补充 partial-header、partial-payload与多fragment晚到测试，覆盖 send响应超时→history查证真实路径，保证无第二次turn/start、无自动ACK、取消继续传播。

## 已核对证据

- `final-python.xml`：121 tests，0 failures/errors/skips；日志明确121 passed，非运行中报告。SHA256 `f6f7e3336454e1552424f3af2093cbe3328148c6798ff7ef486f9d3fe40fe53e`。
- `consumer.xml`：3 tests，0 failures/errors/skips；SHA256 `0d2546532b1aeb224206efccedb1ee6e6c7265d02fcf02218293b260b692f1fd`。新增消费者允许awaiting_history，变更范围合理。
- `go-race.log`：核对JSON事件，nativebridgecli 10 test pass，0 fail/skip，package pass；SHA256 `43e85948b7e57dbe9e55b77399c7c3d4184f91fc9bdb9e615927210bd9e720ad`。
- `go-vet.log`：空日志hash `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`，`verification.json`退出0。Go使用1.26.10显式PATH/GOENV off/GOTOOLCHAIN local/GOPROXY off；本审查未重复Go构建。
- manifest SHA256 `6aa72ed0aed5902e7948322e07dab56f97495723fc9793efe09ccdb08ee15d81`；diff SHA256 `d9b28b6bd2f757d4e53f04c50085bcb2c3ee8ac2bac4cb0d2a6f1c0ac3fd2dd5`。

## 覆盖边界与复验要求

DEL01/03/04部分有deadline、packet限额、严格history、原ID无重发的protocol-fixture证据；DEL02有真实本机进程/Unix socket的synthetic backend延迟history测试。不能将这些称为真实Codex忙碌父会话验收。

DEL05尚未完整证明：新增重启测试实际只调用共享 `observe_delivery` helper + 重新创建adapter，未让 `run_driver`/`run_helper` 在sending、response、proof与ACK各checkpoint OS kill后重启。已有ACK请求重放单元测试不等于全driver crash恢复。必须补相应公开路径故障注入/恢复验证，尤其proof成功未落盘、Go ACK成功未记录；明确核对发送数、同command ACK、恢复后receipt/status及owner退出清理。还应覆盖ledger已controller_acked但driver ack receipt/status未写的窗口：当前open_batches排除controller_acked，恢复可能直接进入无待发事件等待，此处尚无独立OS复现，不能声明通过。

结论：当前冻结树不满足T05验收，需修复P1并补关键恢复证明后再审。未覆盖的native/live、OS kill crash与完整项目回归保持未验收；无提交/push/全局安装变更。

复现日志 SHA256 `3642c61343e83d2ea85cda9e0d769a3b451eb81c5798b15c573f0f7619e19887`。
