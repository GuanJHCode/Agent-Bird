# native-tui-01 恢复失败定位与修复

原 case 来源 MAIN 3f1c2df，记录保持 UNKNOWN，不改写。恢复连接的安全投影已得到同 P 的 resume request5/response、home match 与零 client turn/start；随后同时有 startup-skills-list-* 和 id9 两个新请求。原 sink 只保存最后一个 request ID，先到的 startup 响应被错误判为不匹配，令 protocol_valid=false；后到的 id9 即使建立 resume_barrier 也无法发布 ready。这是门禁错误，不是延迟或认证问题。

修复只把后置 skills 请求逐 ID 配对；已配对的并发成功响应不互相覆盖。原 epoch、owner UUID、home、方向、成功响应、零 turn、deadline 与 UNKNOWN 规则不变。真实 WS 帧 RED 复现相同并发顺序；独立 dummy TUI 现在并发发两次 skills，再通过同一跨服务/分页/Go ACK 组合。

对应全链 RED 另观察到 first-quit 读取直接写入的 final JSON 时得到半成品。backend-final 和 activation-final 现复用既有 immutable publisher，路径/字段/O_EXCL 不变；受控发布窗口仍仅 pending，完整 owner0600/nlink1 才读取。实际服务测试记录四次 final 经过 publisher。

本次只读核实 TUI40044/40225、service40032/40217、backend40082/40250、guardian40071/40249 均已退出，两条 record.private_socket 均不存在；没有发送任何信号。原 case 的 cleanup 未充分确认仍原样保留，由 root 独立 postflight 补证，不追改历史状态。

HTTP 第二次请求409发生在首会话的 temporary-structured thread/turn，早于恢复；固定源码 tui/app/thread_routing.rs:1982 和 app/event_dispatch.rs:2708–2726 显示自动标题生成的临时请求路径。不能把总 HTTP 数写成2：后继成功需 accepted_requests=2，额外背景请求如实记录为合成 endpoint 拒绝；owner 恢复零 turn、完整 history/ACK 门禁不放宽。没有外部模型或真实账号调用。

作者未运行新 native/launchctl；对应证据见 native-resume-skills-{red,green}.log 与 native-final-publication-red.log。真实重测仍由 root 新 case 单次准入。

最后源码修改后：34 passed /9.98s，保留原 fork 的6条 DeprecationWarning。命令沿 native-tui-combined.md 的固定 safePython/pytest 环境，选择 `test_native_resume_gate.py`、`test_native_tui_case.py`、`cold-start/tests/test_activation_protocol.py` 三文件。未扩大旧全套。
