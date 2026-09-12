# sd07：固定当前普通入口 0.154.0

主合同 `263de78` 授权未消费的 sd07 按当前普通入口适配；本提交基线为 `0691cca`（含 deadline 修复 `f27cec2`）。

native 侧增加固定 `CLI_VERSION=0.154.0`，将二进制 SHA 固定为 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`。线程元数据、TUI 连接版本及新 binding 统一引用该值。当前模拟 fixtures 同步到 0.154.0；历史 sd03 原始 binding、发送意图及摘要回归均未改。

先执行 [RED](native-sd-qual-07-version-red.log)：旧代码拒绝当前 SHA、接受旧 SHA，并无法接受当前线程/日志版本。随后最小版本更新，执行 `python3 -B -m unittest native_sd_control_test native_sd_collect_test native_sd_pty_test -v`，44 tests PASS，见 [GREEN](native-sd-qual-07-version-green.log)。回归覆盖旧 SHA 与未知 SHA 在 case 创建/服务启动前拒绝，以及当前证据中混入旧 TUI 版本拒绝。完整 helper binding 的新旧版本/摘要组合由 helper 的独立工作树验证。

[只读版本前检](native-sd-qual-07-preflight-version-pin.json) 已通过：普通入口的实际二进制 SHA 和固定 Go SHA 一致，默认 socket 不存在。本前检没有启动原生服务、TUI 或模型，不取代整合、复核后临近 RUN 的新鲜前检。用户当前保护文件只读取摘要，未覆盖或回退。

`git diff --check` 退出 0。没有新增待删除的临时文件；原有 untracked `scripts/__pycache__/` 保留。sd07 nonce、单次 paste+Enter、原 10/120 秒期限、family/root/Go owner、canonical digest、单次 toolOutput 和 uncertain 不重发判据保持不变。

冻结源码 SHA256：

- `native_sd_control.py`: `224e28f2b3ec99b7d424fe7f8490b3a769f11793eb71518a3a1cc37539a5aab5`
- `native_sd_collect.py`: `485e2b569cba0bdc8f5a78f408508cb07e75ef9073e8dc6efe6c39645ecb012c`
- `native_sd_pty.py`: `0ec348a89cfe1d5b41e23885f20a4323d2631af300ad577fa2e8726e16d8d5c5`
- `native_sd_clock.py`: `c16c8d0d0d9b1652d18606230351493f3cea33211f8b4d3323bb5b6bf2466087`
