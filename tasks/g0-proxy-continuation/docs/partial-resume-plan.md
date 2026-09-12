# partial task → stop → quiet resume → explicit continue

本地阶段只准备证据门禁和新 case/job 方案，不创建目录、不启动 native、不发送 prompt。

建议使用全新的 `proxy-partial-01` nonce、case `tasks/g0-proxy-continuation/data/proxy-partial-01`、job `tasks/g0-proxy-continuation/data/proxy-partial-01-job` 和独立 preflight。它不能引用 `sd-qual-08` 的 case、job、nonce 或 thread。模型阶段必须在实际动作前保存有效配置证据，确认 effective model 为 `gpt-5.6-luna`、reasoning effort 为 `medium`；静态 TOML 或默认值不能替代这项确认。

操作链固定为：

1. 创建 O_EXCL preflight，记录完整绝对 argv、源码/binary/config/hook/seed hash、空 case/job/endpoint、10 秒本地窗口和 120 秒模型窗口。新 task 的初始交互仍通过普通 `codex` 入口完成；本 runner 不注入 prompt。
2. 在获得有效 Luna/medium 配置证据后，执行一个固定最小任务，持久化一个未完成 partial checkpoint。checkpoint 绑定新 thread、task revision、segment、完成步数、effect count 和 prefix hash；完成步数必须小于总步数。
3. 停止前记录 Go worker 与其直接 parent 的 PID、出生身份和 executable，并验证 direct parent 仍是登记的 TUI/controller。同步记录 NativeRuntime 登记的 backend/TUI `OwnedProcess`、两个 `OwnedEndpoint` inode，以及 checkpoint hash。parent 变化、reparent 到 PID 1、PID/executable/birth 不匹配、endpoint 替换或 checkpoint 改变都拒绝 stop 证据。
4. TUI quit 后只验证本任务登记的 backend/TUI 退出和 endpoint 清理；不按 uid、exe 或 ancestry 泛认领 descendants。若中间存在 shell，必须以 `exec` 消除或在实际证据中确认它的 direct parent 关系；controller 被 SIGKILL 的场景另行登记，当前最小样本不宣称覆盖。
5. 用同一 thread 的新 NativeRuntime 连接恢复，观察 10 秒静默窗口。`ResumeAttachmentInferer` candidate 必须来自成功的 `thread/resume` response；status 只做精确 ID 对照。trace 必须完整，client `turn/start`、client `thread/start`、server `turn/started` 都为零，模型 turn 计数为零；允许已核验同 ID 的 optional server `thread/started`。
6. 只有收到独立、明确的继续输入且 effective Luna/medium 证据仍有效，才进入新 segment 和 120 秒模型窗口。新 segment 继续旧 prefix，不能重做已提交 effect；继续前后的 checkpoint、segment、effect count 和输出 hash 独立对账。

`partial_resume_plan.py` 提供纯离线 `validate_partial_stop`、`validate_resume_quiet` 和 `validate_continue_gate`，以及不创建 case/job 的 O_EXCL plan preflight。它把 parent guard、直接 owned process/endpoint 和 durable checkpoint 分开记录，不能由其中任何一项推导通用 process tree ownership。实际模型运行仍需主会话单独准入和新鲜配置核验。
