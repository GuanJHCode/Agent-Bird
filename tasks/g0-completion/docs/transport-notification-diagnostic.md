# 未列通知的安全诊断增量

基线 `79aadbb35873ca3b9adfa7c055768b946ddc19d7`。本次只新增诊断投影，不修改 SAFE_METHODS、RPC 顺序、原 10/120 秒窗口或 UNKNOWN 语义；未运行 native、读取真实凭据或修改 native-transport-01 原 case。

原生 01 仅有 195-byte canonical envelope 摘要，不能据此确定方法名。未把任何猜测写成根因，也没有放行新的通知。下一个独立 case 的 `envelopes` 中，`method=unlisted` 记录增加：

- `unknown_method_sha256`：方法名 UTF-8 bytes 的 SHA-256；
- `method_length`：同一 UTF-8 bytes 的长度；
- `diagnostic_known_method`：只有名字精确属于固定官方 ServerNotification 枚举时才出现；不属于则省略，绝不保存未知名字原文。

诊断方法集与放行集独立。官方固定 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 的 `protocol/common.rs:1879` 定义了 83 个通知方法（包含显式 serde rename）；原文 SHA 为 `6aa47ec984c9198ea797bf63b784cf6a88cc0ab85efb0a4ac75e01aabffc2cfa`。运行时对同一份有界 bytes 校验此固定摘要后才提取方法名，读取拒绝末级 symlink 和非普通文件，失败退回 hash-only。该来源文件也加入 source_pins。诊断集合从未传入 receive 的准入条件。

真实 Python dummy/UDS/WS 失败回归覆盖疑似私密方法名和一个官方但未放行的方法 `thread/goal/updated`。后者带 emittedAtMs，仍 UNKNOWN、零 turn/零 endpoint 请求；前者结果中没有方法名原文。两种情况均有自有进程清理证明。该例只验证诊断行为，并不表明它是 native-transport-01 的实际方法。

TDD：新增两例先因缺 `unknown_method_sha256` 失败，`2 failed, 13 deselected in 0.80s`，见 `transport-diagnostic-red.log`。

新鲜组合：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_synthetic_native_transport.py \
  tasks/g0-completion/tests/test_synthetic_native_fixture.py --tb=short
```

结果 **33 passed in 4.79s**，见 `transport-diagnostic-green.log`。真实 native case02 由 root 另行准入和编排。
