# 允许已确认的 remoteControl 状态通知

基线 `982cfa77360c4b25c5b19019bffc360707f260c1`。主会话 native-transport-02 已精确确认此前未列方法为 `remoteControl/status/changed`；本次不改动 01/02 case，也不执行 native。仅允许该后台通知，不增加任何 client RPC，尤其不调用 remoteControl enable/disable 等写方法。

固定官方源码 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`：

- `protocol/common.rs:1947` 将方法映射到 RemoteControlStatusChangedNotification。
- 新补取 `protocol/v2/remote_control.rs:30` 定义 camelCase payload：必需 status、serverName、installationId；environmentId 为可选字符串。
- `:161` 给出状态 disabled、connecting、connected、errored。

实现只检查该 payload 的字段集合、必需字段、状态枚举与字符串/可空字符串类型。没有 id 的 server notification 才可进入原等待循环；携带 id 仍按服务器请求拒绝。emittedAtMs 保持兼容，不以状态通知代替 thread/start 收据，也不改变 task owner。

记录仍只有 method、整数 request id（通知为 null）、canonical envelope bytes/SHA；serverName、installationId、environmentId 或其正文不落盘。其他未列方法仍 UNKNOWN，原 10/120 秒窗口、一次发送与 owned cleanup 均不变。方法 SHA 独立计算与主会话一致：`fc90be8d31a9f1bb24948acd35f025e91f103bcde03fb735388a7169f1262d24`。来源文件摘要、URL 与行号见 `transport-remote-status-source-receipt.json`。

真实 Python dummy 回归：thread/start 响应前先发该通知，再走两轮 WS→HTTP→history 流程；成功用例确认先后次序、2 个 synthetic 请求、没有 client remoteControl RPC、敏感占位正文未写入结果及自有清理。四个负例覆盖非法状态、缺 serverName、错误 environmentId 类型、携带 request id。原其他未知通知回归也在同进程运行。

TDD：旧代码正常通知失败（1 failed/4 passed）；只加方法名后畸形 payload 的 3 例失败（3 failed/2 passed）；再补结构校验。日志保留在 `transport-remote-status-red.log`、`transport-remote-shape-red.log`，仅去除 pytest 行尾空格。

最终命令：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_synthetic_native_transport.py \
  tasks/g0-completion/tests/test_synthetic_native_fixture.py --tb=short
```

**38 passed in 6.45s**，见 `transport-remote-status-green.log`。仅 dummy，无 native/model账号/真实auth/launchctl；原生第三 case 仍由 root 另行准入。
