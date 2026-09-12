# 单样本 standalone helper 独立复核

**最终结论：固定 `c3f35d37506f0681e8b02005ac28a3d04b52a85d` PASS，可作为已约定固定 root 单样本实验的 helper。** 无未解决的 P1/P2。两项核心 P2 已在 `5f2bd866` 前修复；随后发现普通 fresh TUI 默认 `paginated` 导致旧 helper 误拒，曾撤回原生准入，现经固定源码核对与最小兼容修复重新通过。controller 及实际原生证据仍另行复核。这不是完整 G0 或通用附着算法通过。

审查链为核心 `0c46f7563ba6692f355daa0b55b951c0d27af204` → RPC/CLI `368d1468e0db8963286a28b5a5aeeaaf6a7f080c` → 修复 `5f2bd86667b0e352e2c170fe5af36e9e9f0b482a` → 模式兼容 `c3f35d37`。源码 worktree、索引、HEAD 均未修改；验证在固定 OID 的独立副本完成。未连接原生服务、调用模型或实际运行服务，没有扩大到 Go 全量测试；后续协议核对只读取固定官方源码。

## 核心发现与闭环

1. **P2：非目标 namespace 未验证即导出。** 核心版本 `native_standalone.py:285–287` 允许非 g0_delivery 的 namespace 对象原样进入历史投影。原输入 `namespace-dict.json` 独立复现该行为；最终版本返回 `invalid_tool_namespace`。修复要求 nullable、有界安全标识。`namespace-str.json` 是合法标识对照，继续允许；这不声称识别合法标识中的任意秘密。
2. **P2：同次完整读取的重复 turn ID 未拒绝。** 核心版本 `native_standalone.py:264–303` 对一条含 delivery/ACK 的 completed 记录，及另一条相同 ID、含 userMessage 的 inProgress 记录，仍给出 completed/ACK 阳性 facts。`duplicate-turn.json` 独立复现；最终版本返回 `duplicate_turn_id`。集合只在一次读取内生效，跨次读取更新仍允许。

原输入在相邻 `../data/`；首审与最终结果分别为 [核心复现](0c46f756-core-reproductions.json)和[最终复现](5f2bd866-reproductions.json)。

## 后续协议发现

**P2，已关闭：fresh root 的历史模式被拒绝。** 固定 `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a` 的 [普通 TUI 初始化参数](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app_server_session.rs#L1908)为非 ephemeral thread 显式选择 `paginated`；L234–238 仅在协议不支持时回退。`5f2bd866` 的 `filter_thread` 只允许 `legacy`，因此既有 fixtures 未覆盖普通新会话的实际默认协议模式。

最小修复保留原 RPC：原生 [thread/read 兼容路径](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/thread_processor.rs#L2992)在 includeTurns 为 true 时持久化已加载线程，并由服务端读取完整历史；[遍历实现](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/thread_processor.rs#L3281)按升序、每轮 Full 遍历至 cursor 结束。`c3f35d37` 只改一行模式白名单，显式接受 `legacy` 与 `paginated`；每轮 `itemsView=full`、数量/字节/时间上限、原生身份和 ACK 门槛保持不变，无 TUI 配置修改或新增分页 RPC。新增测试验证完整模式可用，并拒绝 summary/notLoaded/None 视图。实际服务返回 shape 仍须本次样本验证，不将源码路径当作已实测证据。源码摘要与[最终增量验证](c3f35d37-delta-validation.json)均归档。

## 完整 CLI/RPC 核对

- binding 严格记录 root/nonce、服务与 TUI 的 PID/UID/出生时间、路径、固定二进制摘要、socket dev/inode、boot 与 controller 证据摘要。机验登记项不升级为通用来源或当前显示证明。
- prepare 实际执行固定 SHA Go binary 的只读 read/inspect；schema、owner、revision、count、event hash、未 ACK 条件一致后才生成 producer/intent。send-once 再实际 inspect，并重建、比较 intent。单元测试中的 Go 数据仍是 fixture，不冒充这条路径已原生执行。
- RPC 仅固定 initialize/initialized、diagnostics、remote-control status、thread/read 和一次空 input 的合法 toolOutput；无账号、模型、cwd、权限覆盖、resume、history 注入、审批应答或重发路径。
- 预连接 AF_UNIX socket、禁代理/压缩/心跳、帧/字节/次数上限与固定错误输出已核对；独立检查安装的 websockets 16.0 实现，传入既有 sock 时拒绝重定向。
- 私有 Case 锁与 O_EXCL/fsync 的 send-attempt 在 wire 前建立；身份丢失消耗该 case，回执不等同持久历史。observe 使用原 attempt/boot/单调时间计算剩余窗口，不重新授予 120 秒。
- 发送前使用原生 idle、明确 prior completed/READY final、全部观察工具/轮次终态；完成观察还要求精确匹配的新末轮、completed/ACK final、root idle、真实 Go ACK handled/effect_count=1。时间戳保持原生 nullable Unix 秒，不靠同秒或模型文字推导先后。
- 输出区分 receipt、有限历史投影、facts 和真实 Go inspect/ACK；恒保留来源/当前显示未独立验证以及 absence/resend 为 false 的界限。

## 新鲜验证

| 检查 | 结果 |
| --- | --- |
| 核心 0c46f756 的既有测试 | 11/11，0.005 s；仅历史基线 |
| 5f2bd866 固定副本的 `native_standalone_test.py` | 15/15，0.009 s，exit 0；前次基线 |
| 最终 c3f35d37 固定副本的 `native_standalone_test.py` | **16/16，0.006 s，exit 0** |
| 原两项失败输入 | 均按固定错误拒绝；合法 namespace 标识对照仍接受 |
| CLI 缺参 | 实际 exit 2，固定 usage JSON，stderr 空 |
| 两个 Python 文件 compile | 成功，不生成 pycache |
| 最终 Python/Markdown/JSON diff 检查 | exit 0 |
| 最终 helper SHA-256 | `a830233c4f57456d9007a8416f7253a00a76c0f32c5c3f68a7187e59790924f5` |

测试保留真实 Case、文件锁、O_EXCL 与 fsync；外部机器身份、时钟、Go 和 wire 在对应单元测试中替换。实际连接、controller 独占 PTY/固定 root、真正 final→idle 后工具完成，以及原生历史/ACK 的同次证据仍需实验确认。文件 IO 与清理开销不据此宣称硬实时保证。

归档只需本目录的 `docs/` 与 `data/`。待主 Agent 复制并校验后，再删除 reviewer 自有 `tmp/` 及这些已归档副本；不动其他 worktree 或共享源码。
