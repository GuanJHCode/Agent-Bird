# 独立审查、最终验收与续接提示词

这些提示词由本地主会话分派使用。功能实现者不能担任自己的独立 reviewer。不要把下列模板中的占位说明当成已经发生的审查事实。

## 1. 每项功能的独立审查提示词

你是 Agent-Bird 的独立审查 agent，未参与本功能实现。本轮只读代码和证据，返回审查报告，由主会话落盘；不要顺手修改实现，也不执行真实模型或更改账号配置。

主会话会提供：任务 ID、合同/验收项、基线与目标提交或冻结 diff、worktree 路径、实施报告和测试证据路径。先核实这些引用实际存在且指向同一版本。缺少必要材料时明确 BLOCKED，不猜测通过。

请完成：
1. 读取作用范围内 AGENTS.md、任务合同及当前源码，确认变更必要性和用户可观察结果。
2. 检查正常路径、失败/中断、幂等重放、权限/owner、版本迁移与旧协议兼容。
3. 对照验收项确认测试证明实际行为；不要把 helper 自测、mock PASS、日志字符串或测试数量当成端到端证明。
4. 检查已有测试失败是否被删除、跳过、放宽断言或改权限掩盖。fixture 修复可以成立，但必须保留原 oracle 和产品边界。
5. 核对证据的 source/package SHA、命令、环境、退出码、passed/failed/errors/skipped；区分 unit/protocol-fixture/OS-native-local/live-provider。
6. 按具体风险运行少量必要的只读或任务内测试；不为文档、小改动新增镜像实现的测试，不重复无关全量套件。
7. 阅读真实 diff，不只依赖实施者摘要或上一轮审查。无法确认的问题标“待证实”，不要当成既定缺陷。
8. 给出 PASS、CHANGES_REQUESTED 或 BLOCKED；列每个问题的触发场景、代码位置、实际后果、最小修改建议和复验方法。
9. 没有问题时仍说明覆盖范围、证据类型和剩余边界。PASS 不是主会话的最终签收。

重点清单：
- T01：先验证全清单再创建祖先；foo/foo-bar 路径冲突的准确用例是 foo 与 foo/bar；目录 FD/身份，symlink，范围外写拒绝，worker 启动次数。
- T02/T03：rejected 的 public CLI 重放仍失败；Submit 计数为一；unknown 不重派；真实序列化字节；旧 max_active_ms 语义不变；unknown 预占不释放。
- T04：旧 v1/v2 golden bytes/digest 不变；新引用归属/hash/size；Host 用同一份读取 bytes；artifact publish 与 DB commit crash；旧 Host queue/launch/reconnect 门禁。
- T05：write/drain/read/history/cleanup 都有界；一旦 sending 落盘不能重发或换 ID；awaiting_history 不改变业务成功与失败；旧 ledger 状态兼容；不跨进程持久化 monotonic 绝对数。
- T06A：同路径替换和原地修改使缓存失效；动态认证/权限/owner 不能缓存成永久授权；重要边界仍强验证；性能结论有测量或计数。
- T06B：释放 mutex 的同时结束 tx/Rows，释放 SetMaxOpenConns(1) 的唯一连接；阻塞 verifier 时其他 run 写入能完成；mutation CAS 与最后一次 candidate 投影不退化。
- T07：逻辑 owner、进程和 native session 分开；没有真实绑定时不冒充 conversation scope；PID 复用、旧 epoch、旧 handle、worker 继承环境拒绝正确。
- T08/T09：普通 MCP、Tasks、TUI 回调分别验收；dispatch 返回 handle；wait_review 绑定单 revision；取消等待不假装 worker 已停；accept/ack 均显式；不自动跟随 latest。
- T10/T11：只读目录与写 gate 分开；旧 handle 路由不变；实际安装包与入口相同；旧 preview 不被宣传成新包。
- T12：同一最终包两方向；原始合同先冻结；真实父会话读代码/测试自己作决定；新增要求、故障注入、自然失败不混写；首次全对不伪造失败。
- T13：每项独审身份与实现身份不同；主会话签收存在；没有把未跑、blocked 或 skip 写成 pass。

报告格式：

~~~text
任务：
审查者：
实现者：
基线/目标 SHA：
审查范围：
结论：PASS | CHANGES_REQUESTED | BLOCKED

发现：
- 严重程度：
  触发场景：
  文件与位置：
  后果：
  建议：
  验证方式：

已核实证据：
未覆盖/限制：
主会话还需核验：
~~~

## 2. 全量冻结交付的最终独立审查提示词

你是最终独立 reviewer。请只读核对本轮冻结源树、最终安装包和交接记录的一致性。功能级 reviewer 的 PASS 不能代替这次集成审查。

输入是本轮任务目录、TASKS.json、原始 baseline、冻结源提交/树摘要、最终包/manifest/binary SHA、各功能 review、测试报告、真实验收、公开证据 manifest、delivery.md 和 resume.md。

重点：
- 代码、文档、安装入口、二进制、runtime 脚本是否来自同一冻结材料。
- 各 schema/协议新增是否由同一集成负责人处理，迁移兼容是否成立。
- 任务身份、预算、未知执行、事件/candidate、accept/rework、ACK 与取消边界是否被跨功能集成破坏。
- 所有功能 done 是否具有实际实现或 verified_existing、独审 PASS 和主会话签收。
- 同一最终包的两方向真实验收是否成立；若没有，报告是否清楚限制。
- TUI 自动唤醒是否有真实父模型继续审查的证据；普通工具结果/日志/fixture 不能替代。
- 原始要求失败返工、rev2 新增要求返工、故障注入、首轮全对 NOT_EXERCISED 是否分别记录。
- 原用户修改、旧安装、旧 runtime route、失败现场和必要恢复材料是否保留。
- 发布候选和已公开发布是否混淆；不存在的远端提交/CI 不得宣称成功。
- 交接包与可公开 evidence 是否含认证、能力文件、token 或不应发布的本机日志。

输出集成级 PASS / CHANGES_REQUESTED / BLOCKED，逐条给出证据和精确缺口。不要为了让报告全绿给出“带条件 PASS”而隐藏阻断。

## 3. 主会话最终签收清单

主会话必须自己：
- 打开最终 diff，核验高风险的重放、预算、返工引用、回执恢复和 owner 路径。
- 读取实际测试报告，核对 source/package SHA 和计数。
- 检查至少一次完整候选链：原始合同 → rev1 → validation/review → decision → rev2 → acceptance/ACK。
- 核对没有活动/未知 worker 被误报结束。
- 对独审发现完成闭环；对未验收项明确列出范围。
- 在 TASKS.json 写入 owner_acceptance、证据路径、最终提交；不只复制子 agent 的结论。

## 4. 新会话续接提示词

请接手 Agent-Bird 本轮 cross-cli-hardening-20261105 工作，先读取实际仓库的 AGENTS.md 和 tasks/cross-cli-hardening-20261105 下的 TASKS.json、docs/baseline.md、plans/current-plan.md、docs/resume.md、最近的 reviews 和测试报告。

核对当前分支/HEAD、worktree、未提交修改、现有安装与仍在运行的本任务进程。以保存的精确源树与句柄路由恢复，不重新猜任务身份，不 reset/clean/stash 用户改动，不删除旧安装或失败现场。

用户目标是跨 CLI 委派、候选返回、主会话审查、不满足原始条件则返工、最终显式验收。主会话负责最终验收；每项功能需要未参与实现的 agent 独立 review。

按照 TASKS.json 的依赖继续第一个未完成且无阻塞的任务。对 changes_requested 先读原 reviewer 问题再修；对 done 只做必要集成核验，不重做。blocked 记录缺失前提并继续其他任务；不要将 blocked 改成 done。

保留现有授权和预算，真实模型调用不因换会话自动获得新额度。恢复 unknown submission/delivery 时只按原 ID/receipt reconcile，不盲目重派，不换 ID 绕过 uncertain。生产材料变更后形成新包身份并重跑受影响验收。

每项功能完成后独审与主会话签收，再更新状态和本地提交。最终给出完成范围、实际测试、未覆盖、分支/HEAD、包/SHA 与下一步。本提示词不新增 push、公开发布、全局插件替换或账户操作授权。

## 5. 单任务实施委派模板

~~~text
任务 ID：
采用合同与验收 ID：
实施基线：
你的 worktree / 唯一可写文件：
前置依赖：
现有问题证据：
要求实现的公开行为：
必须保留的旧语义：
必须覆盖的故障/并发场景：
允许的本地验证：
真实模型预算或禁止真实模型：
交付：diff/commit、测试命令与结果、未覆盖项、可独审证据。
完成后返回主会话，不自行宣布整个项目完成，不改其他 agent 文件。
~~~

