# 原生 Goal 编辑与已排队完成动作的竞态

**单个真实样本复现了旧工具单元把编辑后的新 Goal 标为 `complete`。** 同一原生 `functions.exec` 从发起到工具结果返回持续 60.051 秒，期间普通 `/goal edit` 已确认新 objective；该工具输出随后依次返回新 Goal 的 `active → complete → complete`。新 objective 要求编辑后重新开始 60 秒等待，旧动作在确认编辑后约 22 秒就完成了它。

这是原生 Goal 状态更新缺少 revision 条件的单样本反例，不代表新目标实际完成，也不是 Host 业务动作。没有修改原生 Codex、配置、权限、账号、Hook 或聊天文件；使用原来的正常 `codex` 入口和专用测试仓库，只执行合成 `sleep 60` 与固定 marker。没有 ACK/Host 调用。

## 被排除的初始样本：模型主动取消

root 为 `01a08e75-cae3-7043-832e-20ebf886aef7`。旧 objective 精确为 `nr-g0-identity-race-old`。首次脚本使用 1000ms 初始 yield，模型随后反复调用原生 `functions.wait`，因此仍有机会处理 Goal 编辑。

03:24:24.523Z 的 PTY 返回确认新 objective 已 active；03:24:36.175Z 模型实际调用 `functions.wait({cell_id:"1",terminate:true})`，随后原生返回 `Script terminated`。这证明的是模型主动取消旧 cell，不能证明 UI/runtime 在 Goal 修改时自动使旧动作失效，不能算底层 CAS 防护。该判定在原始 120 秒窗口内已形成。

证据：[第一样本原生类型、wait 参数与 Goal 快照](identity-race1-events.json)、[有限 UI 与进程观察](identity-race-ui.json)。

## 反例样本：整个等待期间保持同一个原生工具调用

在同一独占 TUI 里正常 `/new`，`/status` 得到新 root `01a08e81-d7c6-7fa3-bfcd-7e8b80ef4d2e`。采集器又核对了该精确 UUID 的原生 `source=cli` 和专用 cwd。

旧 objective 精确为 `nr-g0-identity-race2-old`。原始请求只要求其 60 秒合成等待成功后完成 Goal。提交的单个工具脚本使用原生 `yield_time_ms:120000`，先创建旧 Goal 并读取，再等待本地命令完成，最后在同一脚本里读取 Goal、调用 `update_goal({status:"complete"})`、再次读取。脚本与原生记录的输入逐字 trim 后 hash 相等，没有等待结束后的模型采样来重新选择这个完成动作。

| UTC 时间 | 实际证据 |
|---|---|
| 03:28:15.176 | 原生 `task_started`，作为固定 120 秒窗口起点 |
| 03:28:25.285 | 开始 `custom_tool_call`，call_id 为 `call_f9RxB9Serb4Wd5hdogszLCBQ` |
| 03:28:39.490 前 | 原生 `/goal` 显示旧 objective、`Status: active` |
| 03:28:41.699 | 原生 `/goal edit` 编辑框打开；未暂停或取消旧轮 |
| 03:29:03.237 前 | 原生 UI 确认新 objective 已 active；工具轮仍显示运行和一个后台 terminal |
| 03:29:25.336 | 相同 call_id 的 `custom_tool_call_output` 返回；本地命令 `exit_code=0`，固定旧 marker 为 `nr-g0-identity-race2-old-DONE` |

新 objective 的确切白名单值为：

> nr-g0-identity-race2-new：请重新启动一次 60 秒等待后才完成；旧目标已经发起的等待不满足新目标。

同一个完整工具输出保留的 Goal 快照顺序是：

1. 初始 `goal=null`。
2. `create_goal` 返回旧 objective、active。
3. 等待前 `get_goal` 返回旧 objective、active。
4. 等待后 `get_goal` 返回新 objective、active。
5. 旧脚本中已排队的 `update_goal` 返回新 objective、complete。
6. 最后 `get_goal` 再次返回新 objective、complete。

这些快照的 thread 都是新 root；旧/新 objective 必须与脚本内的两项已知值完全相等才导出。完整 native output 为 2185 UTF-8 字节，SHA-256 为 `32d82040b6475dd40a549edd841fbccc24465f3a1cfb3255614807be7a49d12d`。同一输出内字段顺序与真正的 exit 0/旧 marker 都保留在 [第二样本过滤证据](identity-race2-events.json)。原请求源码和其 hash 比对保存在 [专用采集器](../../../../tasks/g0-native-return/scripts/identity_race_collect.py)。

新 Goal 在旧动作执行前已经存在且 active，排除了“编辑直到工具结束后才应用”的解释。反例输出 03:29:25.336Z 之前，没有第二个匹配旧 cell 的调用或 `terminate`；后续调用在反例之后，不能解释此前新 Goal 的 active→complete。不能用第一样本的模型主动取消来解释此反例。原生 writer lock 只限制同 thread 的另一个写入客户端，也不限制同一个线程里已经排队的 Goal 完成动作。

## 时限、过滤与剩余边界

两个原生事件导出都严格固定为 `task_started` 起的 **120 秒**。第二样本的错误状态转换在起点后 70.160 秒已出现，结论不依赖窗口外的输出。之后原生整轮 `task_complete` 分别为 147.571 秒和 180.065 秒；它们不是“120 秒内模型完整完成”的通过证据，未据此扩大原竞态窗口。后续 `/goal` UI 仍显示第二样本的新 objective 为 complete，只作为单独的最终状态观察。没有证据足以指定整轮超时的具体原因。

此前 `runner_collect.py` 的逐行 JSON 解码不能覆盖本样本含长 objective 的多行 JSON；它未被修改。新增 `identity_race_collect.py` 只针对两个硬编码的自有 root 和固定时间窗，解码完整 JSON 对象后，仅导出精确匹配的旧/新 Goal 标签、最小状态字段、native call_id/类型、有限 wait 参数、固定 marker、exit code 与 hash。推理和任意工具正文不导出。原 cell 源码必须与本次明确请求一致，且每个窗口只能匹配一次。

结论限于本机 `codex-cli 0.153.4` 的这条正常原生路径。Go 层 owner/revision ACK 守卫不能原子保护这里的 `update_goal`，也不能撤销已经发生的 Goal completion。后续 ACK 身份隔离样本应独立判定，不能把它们作为修复该原生竞态的证据。
