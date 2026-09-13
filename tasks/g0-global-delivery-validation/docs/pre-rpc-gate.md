# Public listener 来源与投递前准入

launchd 创建的 listener 在 client 侧可报告 listener 创建者的 UID/PID，不能据此认定实际服务进程。0RPC 唤醒保持既有 9adb5dc 诊断规则；此次只改变受控 Python owner/helper 的全局实验入口。服务端 accepted frontend 身份、grant、profile、私有 backend 验证与 RPC 白名单均保持原规则。

manifest 和 config 的 external_admission 完全一致且冻结为 launchd 或 inherited_fd。后者继续核完整 public peer；前者只保留 listener provenance。HTTP 握手完成后 child 输出 public_connected 并等待 parent；此时 owner 没有 initialize/thread/start，helper 没有 Go prepare/claim 或任何 RPC。controller 从已固定目录/FD 读服务 immutable owner-connected-lease-pid 或 helper-ready-lease-pid，核完整 service/job/kernel、frontend、backend/socket、profile/context 与实际 grant SHA，才回 admitted。owner-ready 必须沿用前置 connected 的同 lease/backend，helper proof 同理。

admitted 只含受限文件名、activation ID、lease 与 SHA，不携带任意路径。child 从 manifest state_dir 稳定读取实际 receipt，与回复的 SHA、manifest/grant、lease/profile/context、服务身份和自身 PID/UID/birth/executable 比较，并再次核父进程身份，之后才执行第一次 RPC/Go claim。旧 private native_delivery_case 缺少 external_admission 时维持原严格 peer 和原执行顺序。

TDD 初始测试观察不到 public_connected，原流程已执行 RPC（pre-rpc-gate-red.log）。真实独立 FD 服务、child pipe、dummy backend 的计数证明两个 gate 前零新增 RPC/零 claim；通过后真实 Go ACK effect_count=1。另一个独立实际 FD/client 测试仅模拟 client 侧 listener UID0/PID1，服务 accepted frontend 仍是实际 OS 身份；不调用 run_global 的 launchd 模式，不替换真实 launchctl。该模拟正例必须读到实际服务回执，错误 SHA/manifest/grant/lease/父进程 birth 拒绝，inherited_fd 仍拒这种 public peer。父端缺回执、错误 frontend/socket/grant 的用例保持零 RPC/claim。

原本地10秒和业务/清理窗口不变；负例测试仅缩短自身等待以有界收尾。服务 publisher 来自 028bd88 → 062a506；本提交不改服务生产代码。不执行 native、真实注册、真实账号或外部模型，不宣称普通 TUI/再次全局验收已通过。新鲜组合证据与完整命令见 runner.md 和 pre-rpc-gate-suite.log。
