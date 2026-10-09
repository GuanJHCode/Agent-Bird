# F01 里程碑记录

contract/process/Host 已完成；Store/公开完整链尚未实现，未声明 F01 完成。

- typed proof 固定原 cmd.Start 边界、Context/完整 Command，单次消费；已启动 Handle 保留原 Wait/Stop。
- Host 发布独立 validation_start 事实，执行 private sealed native 入口；冷恢复不重建 live proof。
- 最终本里程碑 `milestone-host-green`：8 top /49 named PASS，exit 0。以下保留所有此前失败，不以中间批冒充 final。

- `contract-process-green`: exit 0; top {'pass': 3, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `contract-process-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 4, 'fail': 2, 'skip': 0}.
- `host-green-02`: exit 0; top {'pass': 5, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `host-green`: exit 1; top {'pass': 4, 'fail': 1, 'skip': 0}; named {'pass': 21, 'fail': 2, 'skip': 0}.
- `host-red`: exit 1; top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `milestone-host-green`: exit 0; top {'pass': 8, 'fail': 0, 'skip': 0}; named {'pass': 49, 'fail': 0, 'skip': 0}.
- `sealed-security-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 0, 'fail': 3, 'skip': 0}.

`contract-process-red`/`host-red` 包含新 API 编译 RED；`host-green` 失败由本任务 sealed 0500 目录的测试清理权限引起，已按 owned path 显式清理修复。`sealed-security-red` 为 malformed native 与 writable sealed 的业务拒绝回归 RED。
实际 cmd.Start EAGAIN/EINTR 使用 per-call 受控 seam 注入，未声明自然内核资源失败。真实 Host+Git+Store 新 attempt 链待后续里程碑；无真实 model/provider 或安装替换。
