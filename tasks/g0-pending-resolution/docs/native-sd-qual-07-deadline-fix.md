# sd07：跨初始期限的 SessionStart 不得触发 paste

基线 `57a3019`。这是本地时限门禁修复；未启动服务、TUI 或模型，sd07 nonce 未使用。

真实 controller.run 的隔离回归固定 origin=1s、initial_deadline=121s，Hook 查找于 2s 开始后，分别在 121s 和 122s 返回有效的本服务 SessionStart。旧实现两个子例均错误写入 `initial-paste`，随后才因同一时限检查中止；没有 Enter。见 [RED](native-sd-qual-07-deadline-red.log)。

修复在 `initial-paste` 前再次读取连续时钟并比较同一 `initial_deadline`；到期记录 `initial_binding_inconclusive` / `initial_paste`，保持零输入，不创建 initial-submit 或发送意图。没有扩大窗口、重置起点、改变 nonce 或增加输入。旧的 10 秒后、120 秒内 SessionStart 仍可 paste+Enter 一次。

验证：

- 新边界回归和两个相邻门禁用例共 3 tests PASS，见 [GREEN](native-sd-qual-07-deadline-green.log)。
- `python3 -B -m unittest native_sd_control_test native_sd_collect_test native_sd_pty_test -v`：43 tests PASS，见 [完整相关检查](native-sd-qual-07-deadline-suite.log)。
- `git diff --check`：退出 0。

本任务未生成新的临时脚本；原有 untracked `scripts/__pycache__/` 保留，未删除归属不明文件。环境版本变化的只读前检单独保留，不据此将本地检查算作原生资格通过。
