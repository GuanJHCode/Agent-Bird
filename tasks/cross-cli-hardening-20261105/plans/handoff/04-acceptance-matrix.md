# 验收矩阵与证据记录

所有条目是本轮待执行的验收合同，默认 NOT_RUN，不是已有测试名称或通过声明。实现者选择有价值的回归测试，优先通过公开接口和可观察副作用证明行为。

状态：PASS / FAIL / BLOCKED / NOT_RUN / NOT_EXERCISED / OUT_OF_SCOPE。层次分别记录 inspection、unit、protocol-fixture、OS-native-local、installed-package、live-parent、live-provider、benchmark；层次名称不得互相替代。

主动回调 PUSH 条目属于对应宿主能力门禁。LIVE-03 若自然场景未触发，保留 NOT_EXERCISED，不能给“全部场景已验收”的结论；确定性返工测试和真实 happy path 仍独立报告。普通 MCP 已加载也不证明 Tasks 或主动唤醒。

核心 PASS 要求必做实现/独审/主会话签收、REG-01～REG-03、LIVE-01/02/04/05 和最终集成审查通过。LIVE-03 单独记录，未自然触发可 NOT_EXERCISED，不替代确定性返工测试。T13 能生成受阻交接，但产品 gate 不因此通过。

## T00：基线与现场

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| BASE-01 | 审查 SHA、本地修改、后续提交 | 有可核对 baseline；未覆盖用户文件；实际实施源树明确；已修复项归因。 | inspection |
| BASE-02 | 独立工作区与写入归属 | agent/worktree/文件归属明确；全量 Go 测试绑定冻结树；已有运行现场保留。 | inspection |

## T01：缺失父目录

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| WS-01 | 已有目录与两级以上新目录的授权文件 | Host 只创建必要祖先；worker 能生成目标文件；无空文件预造；Codex/Claude 分别验证。 | unit + OS-native-local |
| WS-02 | foo 与 foo/bar 冲突、禁止路径、symlink 或替换 | 在 worker 启动前拒绝，启动计数零；没有落在 worktree 外的创建。 | unit + OS-native-local |
| WS-03 | 同目录未授权邻接文件、Git metadata、atomic 残留 | 精确写边界与冻结规则继续拒绝；不能通过目录准备扩大 scope。 | OS-native-local |
| WS-04 | 准备失败与恢复 | 保留归属记录；只处理本任务新建且身份一致的空目录；不递归删未知内容。 | protocol-fixture |

## T02：派发重放

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| DIS-01 | 第一次明确 rejected，第二次同请求 | public CLI 两次业务失败，第二次给 rejected；fake coordinator 的 Submit 总计 1。 | protocol-fixture |
| DIS-02 | 提交已接受但回复丢失 | 保持原 request/control；reconcile 恢复同 run；无新增 Submit/attempt。 | protocol-fixture |
| DIS-03 | 同 ID 不同内容、旧 rejected 缺 error、未知 status | 冲突或明确历史拒绝；不猜错误、不返回 existing 成功；无身份/预算重置。 | protocol-fixture |
| DIS-04 | 拒绝 checkpoint 写失败或提交各 crash 点 | 没有先删恢复材料；结果不确定时保留核对路径；没有隐式 retry-admission。 | protocol-fixture |

## T03：预算与范围

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| BUD-01 | attempts 边界 0/1/2/3/4/64 | 各入口一致；超范围在提交前拒绝；不为统一而开放 64。 | unit + protocol-fixture |
| BUD-02 | 单段 120 秒、总 180 秒，第一段使用 110 秒 | 下段最多获授 70 秒；用可控时钟验证，无需真实等 180 秒。 | unit |
| BUD-03 | 技术 retry、返工、unknown、重启/rebind | 计数/修订语义不变；budget_group 权威、同组多 task/拆分不能各获一份总额；unknown 预占不复用；重启不补满。 | unit + protocol-fixture |
| BUD-04 | 旧 v1 任务与失败迁移/旧 coordinator | 旧每段语义与默认组总限保留；迁移原子；新总预算遇旧 coordinator 必须拒绝，不能自动降级变宽。 | protocol-fixture |

## T03：序列化限制

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| LIM-01 | 中文、引号、反斜杠、字段边界、超大文件 | 按真实 JSON/wire bytes 检查各层上限；不改旧 hash；过大不启动 worker、不截断验收标准。 | unit + protocol-fixture |

## T04：返工引用与兼容

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| REW-01 | 长反馈、多轮历史、原始 brief | 只存权威正文；launch payload 有界；原任务与前轮有效条件不丢失；不靠模型自动压缩合同。 | unit + protocol-fixture |
| REW-02 | 旧 v1/v2、重复/冲突 command、ACK 后审查 | 旧 golden bytes/digest 不变；原 receipt 精确重放；accept/rework 互斥；最后 attempt 候选仍可审查。 | unit + protocol-fixture |
| REW-03 | 引用缺失、截断、symlink、篡改、跨 owner/revision | Host 对同一份有界 bytes 验 hash/parse；拒绝错误归属；worker 不读取控制目录。 | protocol-fixture + OS-native-local |
| REW-04 | artifact publish/DB commit/reply crash；quota；Host 降级 | commit 不指向未发布正文；staging/发布未 commit 均有界计费和在途保护，GC 不删将提交引用；已 pin 不删；queue/launch/replay/reconnect 门禁。 | protocol-fixture |

## T05：回执桥可靠性

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| DEL-01 | send/drain/response 不结束或不断收到无关通知 | 各阶段有界；packet/内存有界；已进入 sending 则 uncertain，无第二次发送。 | protocol-fixture |
| DEL-02 | 主会话忙碌、history 延迟超过旧 2 秒和单轮期限 | awaiting_history 可恢复；不把 worker 标失败；后续精确 history proof 后前进。 | protocol-fixture + OS-native-local |
| DEL-03 | 回复丢失但历史后来存在；重启 driver | 只按原 ID/hash/owner 查历史；不重发、不换 ID；monotonic deadline 不跨进程比较。 | protocol-fixture |
| DEL-04 | 错 thread/epoch/hash、重复标记、晚到重复响应 | 不混淆 RPC；拒绝错误 proof；不自动释放 uncertain 槽或 ACK。 | protocol-fixture |
| DEL-05 | sending、response、proof、Go ACK 各阶段 crash/owner 退出 | 只依持久化事实恢复；同 command ACK 重放；不误投新 owner；cleanup 有界，worker 停止另走确认。 | protocol-fixture + OS-native-local |

## T06A：探测与等待开销

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| PERF-01 | 长时间等待决策 | 计数证明不再每轮全量 hash 三个 executable；取消与失联仍可响应；不预设收益百分比。 | benchmark + protocol-fixture |
| PERF-02 | 同路径替换/原地修改/权限与 policy 变化 | cache miss/失效正确；初次与关键动作强校验仍执行；认证/owner/权限不被缓存成永久授权。 | protocol-fixture + OS-native-local |
| PERF-03 | 相同环境重复 probe、长任务进度 | 报告 probe/hash/spawn/first_event/history 的时延/计数；无 progress 不误报失联；同机对照。 | benchmark |

## T06B：inspect 并发

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| READ-01 | run A verifier 人为阻塞，同时 run B 写 DB | 在不释放 verifier 前 run B 写操作已完成，以因果证明 mutex/唯一连接均已释放。 | unit + race |
| READ-02 | verify 期间 accept/rework、预算/provider/Host 变化 | 丢弃过时动作投影或有限重读；mutation 不接受旧 tuple；有效候选与返工资格分开。 | unit + race |
| READ-03 | 取消 inspect、多并发验证、Git 失败 | 无状态变更、连接泄漏、无界子进程；最后一次候选与既有互斥回归通过。 | unit + race |

## T07：owner 与会话

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| OWN-01 | PID 复用、错 birth、旧 epoch、失效 capability | 无法认领旧任务或误投回执；进程与逻辑身份均核验。 | protocol-fixture + OS-native-local |
| OWN-02 | 同一进程多聊天、clear/resume | verified_native_session/显式 owner 轮换验证聊天隔离；managed_process 无法观察的切换不保证隔离，保留 collect/rebind，未证实路由的主动推送关闭。 | protocol-fixture；native claim 需 live parent |
| OWN-03 | controller 重启、旧 handle、安装升级 | 显式 rebind/recovery；原 runtime 路由保持；不换 owner/epoch 绕过 uncertain。 | protocol-fixture |
| OWN-04 | worker 继承环境与父退出 | worker 不能递归 dispatch；父退出按当前合同停止并保留现场；unknown 不假装已结束。 | OS-native-local |

## T08：普通 MCP 接口

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| MCP-01 | dispatch/inspect/wait/collect/决策/ack/stop | 与 CLI 同一业务结果；dispatch 返回 durable handle；结果有界，分页/cursor 正确。 | protocol-fixture + installed-package |
| MCP-02 | 伪造 owner/control 或跨任务 review token | 不能覆盖可信 owner，token 不代替 owner；过期 revision 新 mutation 拒绝；同 command/同正文且 owner 权限有效的重放返回原决定，即使 revision 已前进；改内容拒绝。 | protocol-fixture |
| MCP-03 | 请求超时、取消、父进程关闭 | 不额外委派；取消等待与 worker stop 分开；没有自动 accept/ack。 | protocol-fixture |
| MCP-04 | 普通调用没有 Tasks capability | 返回普通 schema，不返回未协商的 Tasks 结果；fallback 可用。 | protocol-fixture |
| MCP-05 | 任务内配置加载、worker empty MCP | 真实父 CLI 加载普通工具；不写全局配置；worker 不能通过 MCP 递归派活。 | installed-package；父加载需 OS-native-local |

## T09：条件性原生回调

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| PUSH-01 | Codex 空闲/忙碌受控父会话 | 回执进入核验过的原 thread；真实父模型继续审查；现有 TUI 接管范围清楚。 | live-parent + OS-native-local |
| PUSH-02 | Claude 满足条件的 Tasks 会话 | 按请求能力协商；同 revision wait_review 完成；真实父模型收到并继续。 | live-parent |
| PUSH-03 | clear/resume、断线、输入关闭、旧 revision | 已验证会话不将旧结果作用到新 revision/另一 owner；managed_process 未证实路由则关闭推送并回退；宿主语义与恢复范围记录。 | live-parent + protocol-fixture |
| PUSH-04 | 宿主/网关/模式不支持、取消等待 | 能力关闭并给原因、wait/collect 正常；不把取消等待当 worker 已停；未原生验证明确 BLOCKED。 | protocol-fixture |

## T10：兼容性合流

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| COMP-01 | 旧 405 与现 46 Claude 目录差异 | 有恢复并验证的 patch 或明确收窄 ADR；catalog/cohort/flags/tests/docs 一致。 | inspection + protocol-fixture |
| COMP-02 | Codex 21 pins 与 coding gates | 原 pins/摘要保留；只读合流不扩大 exact writes；未知摘要拒绝。 | unit + protocol-fixture |
| COMP-03 | 旧句柄和未知/新增版本 | 旧路由保持；版本准入不冒充逐版 live；无运行时追最新的隐式变化。 | protocol-fixture |

## T11：最终包与文档

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| PKG-01 | 冻结源码→包→安装→入口 | source/binary/manifest/package/receipt 绑定唯一；运行实际已安装 wrapper。 | installed-package |
| PKG-02 | 安装到新的私有路径及空格路径 | 安装/doctor/入口加载成功或有正确支持边界；不覆盖全局插件/旧版本。 | installed-package |
| PKG-03 | 移动安装、缺文件、错误解释器或摘要 | 按现有 receipt 合同明确拒绝/要求重装；不从源码或 latest 静默替代。 | installed-package |
| PKG-04 | 验收中途更改生产材料 | 新构建身份；受影响验证重跑；旧报告标历史，不拼成同一包 PASS。 | inspection |
| DOC-01 | 当前设计、安装、README、矩阵、历史文档 | 公开包/候选包、scope、认证路由、read/write、fresh/resume、delivery/evidence 逐项准确。 | inspection |

## T12：同包真实闭环

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| LIVE-01 | Codex→Claude，多文件+新目录 | 真实父与 worker；同最终包；父读取合同、diff、同候选测试，自行决定；公开入口收尾。 | live-provider |
| LIVE-02 | Claude→Codex，同样任务类型 | 与 LIVE-01 相同 runtime/package SHA；独立记录；原工作区不被直接改写。 | live-provider |
| LIVE-03 | 原始条件不满足→真实父审查反馈→rev2 | 全部原条件首次派发前冻结；反馈引用具体失败；rev2.base=rev1.candidate；父复审；未触发写 NOT_EXERCISED。 | live-provider；fault-injection 单列 |
| LIVE-04 | 首轮全对、新增要求返工、故障注入 | 三种证据分别记录；不伪造首轮缺陷、不暗改冻结候选、不无限追加付费试验。 | inspection + 对应实际层次 |
| LIVE-05 | accept/ACK/最终退出与集成 | accept、ACK、集成各自明确；active/unknown 核对；只在任务私有仓进行已授权整合。 | live-provider |

## T12：最终回归

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| REG-01 | 冻结树 Go 普通/race/vet/build | 命令与退出码、source hash 完整；失败根因闭环；并发构建期间不修改树。 | unit + protocol-fixture + OS-native-local |
| REG-02 | 相关 Python、原生本机、认证守卫 | 新报告精确计数、skip 原因；fixture/oracle 修复有解释；没有用放宽产品权限制造 PASS。 | protocol-fixture + OS-native-local |
| REG-03 | 原报告、故障证据、凭据处理 | 失败保留，新记录不覆盖；公开证据无密钥/control；SHA/计数可独立复核。 | inspection |

## T13：最终签收

| 验收 ID | 场景 | 必须观察到的结果 | 证据层次 |
| --- | --- | --- | --- |
| FIN-01 | 每任务实现→独审→主会话签收 | reviewer 与实现者不同；具体 findings 闭环；done 有证据和目标版本。 | inspection |
| FIN-02 | 完整冻结交付独审 | 集成一致性 PASS 或准确 BLOCKED；无用子 agent 摘要代替根会话验收。 | inspection |
| FIN-03 | 本地提交、包、续接记录、发布边界 | 分支/HEAD/SHA/未覆盖明确；可继续提示词；资料 prepared 与 core PASS/BLOCKED 分开；未授权发布/全局替换未发生。 | inspection |

## 单条证据记录模板

下列 null 表示未执行或未收集，不能填 0 冒充通过。每个验收 ID 可以对应多份报告。

~~~json
{
  "acceptance_id": "DIS-01",
  "status": "NOT_RUN",
  "evidence_level": "protocol-fixture",
  "source_commit": null,
  "source_tree_sha256": null,
  "package_version": null,
  "package_sha256": null,
  "binary_sha256": null,
  "platform": null,
  "provider_version": null,
  "model": null,
  "auth_route_type": null,
  "command": null,
  "cwd": null,
  "exit_code": null,
  "counts": {"passed": null, "failed": null, "errors": null, "skipped": null},
  "report_path": null,
  "report_sha256": null,
  "observed_effects": [],
  "limitations": [],
  "review_ref": null,
  "owner_acceptance_ref": null
}
~~~

## 有价值的验证方法

- 派发：public CLI + fake coordinator，直接数 Submit/任务/attempt，不只断言 helper 字符串。
- 读锁：阻塞 verifier 的同时让另一 run DB 写完成，以因果关系证明资源已释放。
- 桥：在明确的持久化/IO 边界注入 crash，用原 ledger 真实恢复，核对发送次数。
- 预算：可控时钟与账本断言，包含 settled、reserved、unknown 和恢复。
- Artifact：文件发布与 DB commit 的先后、重复请求、配额和保留规则，不能只测 JSON roundtrip。
- 原生权限：真实 OS 沙箱中验证目标文件可写、邻接文件不可写；fixture 不能代替 OS 拒绝证明。
- Live：真实父会话做实际审查与决定，脚本只提供工具；若模型首轮正确，保持事实。

最终报告以功能/风险覆盖为主，不以“数量必须等于旧 240”作为通过目标。

