# G0 剩余项独立复核

结论：**idle 研究 `497972ee2eeeb46252297c3bcdefd9655fe2b404` 与离线检查器 `f928e87` 均 PASS，Ready to merge: Yes。** 无剩余 Critical、Important 或 Minor finding。这里通过的是指定研究产物及离线检查逻辑，不是原生接入、当前 TUI 附着、真实交付事务或完整 G0。

源 worktree、索引、HEAD 均未修改。本 reviewer 没有安装 LaunchAgent、运行 launchctl、改套接字或配置，没有调用原生模型，也没有重跑 Go 全量测试。Python 验证使用固定 OID 的独立 archive 副本。

## idle 研究与机制报告

范围：`cd3d054..9c473665fda3b98416230cb336c0861714459e4c`，以及后续 `497972ee2eeeb46252297c3bcdefd9655fe2b404`。

- 原设计并不要求旧 pending tool call。独立核对官方 App Server 文档和固定源码后，确认 standalone `turn/start.toolOutput`、`input=[]`、原生 `functionCallOutput` 与 busy 入队的表述准确。最小 `thread/resume` 也不是一律越界，须保持有效设置并满足原服务及所有权前提。[官方 App Server](https://learn.chatgpt.com/docs/app-server#start-a-turn)
- 独立抽查 exec 完成事件、child completion 的 `trigger_turn=false`、root follow-up 限制、durable-sleep 门槛、async Hook 与宿主 extension 注册边界。报告没有把这些静态路径标成新的原生成功样本。
- 50 份源文件的长度、SHA-256、固定 URL，以及 25 对完整文件比较均一致，其中 13 对相同。4 组／8 段局部 hash 也一致；该局部编码是 UTF-8、LF 连接各行、不含末尾 LF。
- 首审发现一个 Minor：旧 alternatives 表把普通 TUI 一概描述为 in-process，遗漏已存在原生 listener 时的普通入口隐式复用。`497972e` 已准确限缩，并链接既有 daemon 实测，finding 关闭。
- 追加的 27 份固定 native 源文件及 3 份本机 Apple man 的长度／SHA-256 全部一致。抽查确认 50 ms 探测仅 connect、实际 UDS connect／WebSocket upgrade／initialize 的各自 10 s 边界、native 新建 bind 与 AddrInUse、stdio 单 ConnectionId 和 single-client 模式。
- 该机制可以研究按需前端与同一 native 私有 UDS 的逐连接转发，但不证明当前 UI 显示 thread；共享 backend 下也不能沿用已验收的直接父退出守卫。报告明确未安装／未实测且新增集成范围仍需合同判断。

初版 26 个本地链接、追加范围 15 个本地链接及对应 diff 检查通过。以上核查使用已有源码缓存，没有重复下载。

## 离线检查器

范围：`cd3d054..c0a0fe3`，报告 `d7c5e33`，最终修复 `f928e87`。

检查器通过精确 thread、原生 item 类型／工具身份、完整 delivery envelope、顺序参与的 hash 及事件版本核对内容；历史匹配与业务 ACK 分开。缺 marker 不证明未接收，所有 ACK 存在也不补造运输阳性。文件／列表输入有界，错误使用固定 JSON，不输出原始载荷。

恒定的 `source_service_verified=false`、`absence_proof=false`、`resend_authorized=false` 与文档范围一致。传入 ACK 只做离线结构核对，不认证来源、事务或实际副作用。报告正确保留六屏障、分页／compact、真实部分 ACK 和原生服务身份的未完成状态。

### 发现与闭环

1. **Important，原 `delivery_audit.py:84–92`：同原生 ID 的类型／工具身份冲突被过滤掉。** 完整 g0_delivery 项后出现相同 turn/item ID 的 agentMessage 或 other_tool，原 CLI 仍返回 `exact_history_marker`。最终版本在过滤前校验稳定 type/name/namespace，再对 G0 envelope 验证内容；没有冻结无关 agentMessage 的合法文本增长。原两份输入在最终版本均返回 exit 2／固定 `invalid_capture`。
2. **Minor，原 `delivery_audit.py:145–147`：已终止的分页遍历被拼成完整页链。** 两页分别以 null cursor 开始且以 null nextCursor 结束，原结果误报 complete。最终版本返回 `captured_page_chain_complete=false`；缺 marker 仍 uncertain，已观察阳性也不会因页链标识改变而被抹去。此 bug 原本也没有把 absence/resend 改成 true。

原始三份复现输入保留在相邻 `../data/`。前后输出见 [首审复现](c0a0fe3-reproductions.json)、[最终复现](f928e87-reproductions.json)，机器记录见 [验证数据](reviewer-validation.json)。

### 新鲜验证

- 原固定版本 13 项测试曾通过，1.064 s；这是首审历史结果，不作为最终源码通过依据。
- 最终固定 archive 副本执行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tasks/g0-pending-resolution/scripts -p delivery_audit_test.py`：**15 项全部通过，1.599 s，exit 0**。TMPDIR 位于 reviewer 自有任务目录。
- 原三份复现输入实际 CLI 重放全部符合修复后的预期；提交中的合成示例输出与存档 JSON 完全一致。
- 最终脚本、Markdown、JSON 的 diff 检查通过。完整 diff 的原始 RED 日志仍有 unittest 输出自带的行末空格；这是原始失败证据的格式警告，已保留，没有改写，也没有将完整 diff 宣称为全绿。

## 归档与清理边界

归档只需本 reviewer 目录的 `docs/` 和 `data/`。`tmp/` 中的两个源码副本及测试 fixture 是可回收材料；依主 Agent 指示，在其复制归档证据后清理。所有本 reviewer 启动的 Python 测试进程均已正常结束；没有保留后台执行。
