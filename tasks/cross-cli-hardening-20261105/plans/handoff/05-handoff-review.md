# 交接包方案复核记录

日期：2026-11-05。
对象：本目录的实施方案、提示词、验收矩阵与任务模板。
产品审查基线：29691e9f08468b1de56cb0c42977084b58c91975。
结论：三路独立方案复核均 PASS，主会话完成交叉核对与交付检查。

本记录只证明交接合同的一致性与可执行性。后续产品代码尚待本地实施；TASKS.json 的任务仍为 pending，测试/审查/产品签收为 NOT_RUN。

## 1. 独立复核范围与结果

| 独立审查角色 | 范围 | 初审 | 修订后复核 |
| --- | --- | --- | --- |
| review2_flow | dispatch、预算、返工引用、旧协议、inspect、对应验收 | CHANGES_REQUESTED | PASS |
| review2_bridges | 目录准备、回执桥、owner、缓存、MCP、任务依赖 | CHANGES_REQUESTED | PASS |
| review2_plan_evidence | 基线与授权、同包证据、每功能独审/主签收、总体状态 | CHANGES_REQUESTED | PASS |

三位 reviewer 均只读审查，交接文件由主会话统一编写和修订。

## 2. 已解决的合同问题

1. 明确 budget_group 为总 attempts/active 的权威作用域；同组不能按 task/revision 重复获取额度；旧端拒绝新预算，禁止自动降级放宽。
2. 返工去重只减少传输重复；Host 确定性保留全部有效条件，不调用模型压缩合同、不静默截断。
3. artifact 的 staging/已发布未提交阶段有配额和在途保护，GC 不得删除即将被决定引用的正文。
4. inspect 释放 mutex、transaction/Rows 和唯一 SQLite 连接；锁外验证后返回前重核；既有写端 CAS/事务保持。
5. MCP 区分过期 revision 的新请求与同 command/同正文的合法重放；重放仍验证当前 owner 权限。
6. managed_process 无法观察的聊天切换不承诺原生会话隔离；未验证路由的主动推送保持关闭。
7. T06A、T08 与第一阶段并行顺序在方案、提示词、TASKS 中统一，编号不代替依赖。
8. T09 代码纳包前需要非 live 测试、独审与主会话代码签收；原生 gate 未通过时保持关闭和 BLOCKED，未审代码留包外。
9. handoff_status 与 core/native 产品状态分开；资料 PREPARED 不构成产品 PASS。
10. T13 可生成受阻交接；上游核心阻塞时产品签收和 task 状态仍 BLOCKED。
11. LIVE-03 自然缺陷未触发可 NOT_EXERCISED；与确定性返工及其它核心必需门槛分别记录。

## 3. 主会话交付检查

- 目标远端分支再次核对为审查 SHA，来源为 GitHub 只读分支查询。
- 15 个任务、62 个唯一验收 ID；全部引用对应；依赖图无环。
- 任务模板默认 pending/NOT_RUN，没有伪造产品通过状态。
- 普通 MCP、MCP Tasks、原生主动回调分别处理；typed worker resume 保持受限。
- 主提示词独立包含核心合同、顺序、审查和授权约束；完整方案提供细节与源码依据。
- 所有文件为 UTF-8，Markdown 代码围栏闭合，JSON 可解析。
- 归档仅包含本交接文档、任务模板和 SHA256SUMS，不包含源码快照、凭据、控制能力或原始认证日志。
- 归档完成后检查 ZIP CRC、包内字节与源文件一致性，以及 SHA256SUMS。

## 4. 本地接手后必须新增的证据

产品实现、单元/故障注入/OS-native/实际安装/真实模型/原生父会话验证全部由本地 Codex 按方案产生当轮证据。本记录不能替代任一产品测试，也不能作为远端已提交或已发布的证明。

