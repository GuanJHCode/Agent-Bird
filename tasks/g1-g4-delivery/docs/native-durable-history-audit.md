# 已停止 native04 的独立持久记录审计

此脚本只读取 native-tui-04 已停止的本任务 synthetic profile，不启动第三个
service、TUI、模型或 RPC，不运行 Go 命令，也不写 claim/send/ACK。原 case
的 `durable_history_verified=false` 和原结果保持原样；新回执只补充
“原生持久记录已落盘且与原投递一致”，不声称第三次恢复演练或断电保证。

固定官方 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 的
`tasks/g0-native-0154-contract/data/source/codex-rs/app-server-protocol/src/protocol/thread_history.rs:81`
说明从持久 RolloutItem 重建 turns；377–398 处理 item-completed、turn-complete
及 rollout 事件。新审计直接核验文件里的记录，不伪造 `thread/read` 响应。

审核内容：同 profile/cwd、CLI 0.154.0、原 thread 与两个已审 turn IDs；
恰好两轮 start/context/complete、四个 completed items、原 READY 输入与
两次固定合成回复。唯一工具结果在 response-item 和 completed-item 两种
持久表示中均须逐字节等于由原 Go intent 验证映射的完整 envelope，并匹配
原 helper proof 的 envelope/intent/claim SHA。Go producer event hash 复用
`tools/g0-return-lab/ack.go:154–169` 的字段顺序；原 ACK 必须仍唯一、handled、
decision_count=effect_count=1，且与原验收记录类型和值一致。

原 backend-final/service-start/frontend/helper 身份须与原 lease 对应，已关闭、
guardian reaped、私有 socket 缺席；PID+birth 的原身份须在审计前后都 gone。
所有文件使用 O_NOFOLLOW/目录 FD/owner/mode/nlink/大小及前后身份、SHA 检查，
总本地上限 10 秒。原 Go/control/result 和 source 文件只读复核后输出摘要；
不导出 rollout 原文或凭据。未知/截断/额外 turn/篡改一律 UNKNOWN，无重试。

作者仅运行 synthetic 文件自测：初始 6 RED；最终 **7 passed / 0.19s / exit 0**。
包括一条完整合成文件链和真实已退出 dummy PID；这不是新的原生或 Go 实验
证据。全部本次临时 synthetic profile 已由测试删除，RED/GREEN 保留于 docs。

主会话合入并检查固定源码后，对已保留的 native04 运行一次（输出目录必须新建）：

```sh
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B \
  /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g1-g4-delivery/scripts/native_durable_history.py \
  --case-dir /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g1-g4-delivery/data/cases/native-tui-04 \
  --result-sha256 c886d6fee6369a9bb12e337de4cca51c20189583d665162af71ca62b91a6b73d \
  --orchestration-sha256 221578cf2202531753899c7c486bfcbec37172b9fb94e9c49bb6f23fd14a2dad \
  --output-dir /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g1-g4-delivery/data/durable-history-audit
```

定向测试（safePython、只读既有 pytest 依赖、不自动加载外部插件）：

```sh
PYTHONDONTWRITEBYTECODE=1 PYTEST_DISABLE_PLUGIN_AUTOLOAD=1 \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B -c '
import sys
sys.path[:0]=["/Library/Frameworks/Python.framework/Versions/3.13/lib/python3.13/site-packages", "/Users/guanjunhui/Library/Python/3.13/lib/python/site-packages"]
import pytest
raise SystemExit(pytest.main(["-q","-p","no:cacheprovider","tasks/g1-g4-delivery/tests/test_native_durable_history.py","--tb=short"]))'
```
