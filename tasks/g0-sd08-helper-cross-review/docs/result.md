# sd08 固定 helper/native 跨组件离线复核

结论：**PASS，无本次覆盖范围内的真实阻塞。**

- helper 固定 `60e13d4e8b47bd461607544f6aedfbfcf3283878`；源码 SHA256 `346ea054176d54fa2f8198e2e57039f029dc575cd97999788002395bae153cc3`。
- native 固定 `7bbc66b968bbf55041f7c74e587b7978fae14fe0`；controller SHA256 `d5b5c00abf45cf5f4c018b085e4b9d756109ad22f450a3e31ca3f73f20f7c862`。所有输入均用 `git show <完整固定提交>:<文件>` 导出，未使用可变 worktree。逐文件摘要见 [inputs.json](inputs.json)。

`python3 -B -m unittest -v native_standalone_test native_standalone_binding_test native_standalone_hash_test native_sd_initial_test`：**44/44 PASS，0.335s**，包括：

- sd02 原夹具克隆，固定最终 native 的真实 `collect.process → control.binding_process → helper prepare`；实际 Go 二进制只对隔离克隆执行 read/inspect。死 PID、socket 和原生 RPC 在测试内替换；历史 CLI/nonce 显式选择，原夹具字节不改。
- sd03 原 prepared 字节克隆，最终 native 实际 `private_json` 序列化、helper `main/issue_once`、controller helper 函数的实际摘要表达式与 `read_send_attempt` 完整匹配；唯一一次虚拟 wire `turn/start`，第二次调用保持不可重发；无原生网络。
- 当前 sd08 单元约束与 native 初始输入边界，覆盖初始 argv、后验双 Hook、不可重置的 prelaunch 120 秒窗口及跨窗/证据改动拒绝。

另执行 [current_binding_gate_test.py](../scripts/current_binding_gate_test.py)：**1/1 PASS，0.252s**。

该测试先运行固定 native 已有 offline initial fixture 中的真实 `control.run`，取得实际生成的 `launch-intent`、`native-origin`、`controller-evidence`。之后在该夹具提供的合成进程观测边界上，执行 production AST 中的 `binding_process`、socket 字段扩展、完整 binding 构造、`private_json` 写入，以及实际 `Popen` 参数表达式；使用未改动的当前 nonce/CLI/Go pin。只有已失效的合成 CLI 路径和 PID 观测被显式替换，不手写另一个成功 binding schema。

真实 helper `main → Case/read → validate_binding → raw evidence SHA → canonical binding SHA → run` 接受上述写入；在 `Rpc.__aenter__` 明确抛出离线边界信号，未建立连接、未返回虚构的准备成功或发送成功。`launch_intent_sha256`、`origin_sha256`、`session_start`、`candidate_hook` 新增键按真实 main 的原始证据读取逻辑接受；原始证据追加一个空格后返回 `controller_evidence_changed`，将 binding/CLI 参数改回 sd07 后返回 `root_nonce_mismatch`，两者都不能再次到达 RPC 边界。详见 [current-chain.json](current-chain.json)。

两组件的 nonce08、CLI 0.154.0、CLI/Go 摘要和 clock identity 一致；当前实际 CLI 与 Go 文件仅只读计算摘要，均匹配固定 pin。helper 本地 10 秒/模型 120 秒和 controller 原始 initial 120 秒窗口保持各自语义。

限制：45 项均为离线结果；当前 sd08 检查证明本地交接契约，不声称原生 prepare/send 或 G0 已通过。没有修改 main/native/helper 运行源码、原 case、配置或全局状态；未调用 native 命令或原生 RPC。测试创建的隔离 case/job 已随上下文退出删除；保留固定源码快照、核验脚本和必要收据。
