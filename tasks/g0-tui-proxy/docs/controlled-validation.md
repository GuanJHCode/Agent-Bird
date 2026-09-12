# 受控代理实验验收

2026-09-12，case `proxy-attach-06` revision-02：**PASS**。主会话负责串行整合与证据核验，Luna 子 Agent 实现和独立审查。

普通 TUI 仍以单一 `codex` argv 启动；临时代理绑定空闲默认 UDS，每条连接独立转发到同一自有 native backend。未修改 Codex 本体或全局配置，未安装常驻服务。

已证实：

- 同 epoch 的 TUI/代理/backend 双 peer 身份链完整。
- 初始协议 candidate 与独立 `/status` 一致。
- `/clear` 得到不同的新会话；两个精确旧会话退订响应之后才观察到 fresh skills replay，新 candidate 与新 `/status` 一致。
- 实际发送正常 `/quit`，退出码 0，随后观察到旧 epoch EOF；无 reconnect。
- 169 条白名单事件，local_seq 1..167 连续及两个方向 EOF；无 gap，model_turns=0。
- CLI、三份源码、全局/测试配置、Hook 配置及程序摘要前后相等；自有 PID 17128/17153 与两个 socket 均已消失，所有实验记录保留。

证据：`../data/proxy-attach-06/result.json`、`trace.json`，以及 `native-registration-06-revision-02.json` 和对应 receipt。原06预登记未执行，由 revision-02 替代；01–05失败及局部成功记录未回写。主会话与独立 reviewer 均核验通过。最后源码组合检查为134 passed，原七份设计文档摘要全部一致。

尚未证明：强制断连后的重新附着、重新登录/重启后续接持久任务、cached side/primary 切换、普通冷启动下代理生命周期，以及完整 G0/G1–G4。05 对空会话的强制断连恢复失败仍保留；06正常退出不替代该结果。

三个本任务 worktree、生成的 pyc 缓存与空 socket 临时目录已清理，分支和证据保留。原 pending-resolution 工作区/缓存和未归属的 `classify_screen` 导出声明保留；该声明已纳入原生运行时实际源码摘要。
