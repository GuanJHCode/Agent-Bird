# 非 macOS 开发交接（2026-10-12）

## 基线与恢复方式

- 本文代码基线：`dbb1a68724f4033557c76f2c21acdf71fb9c1970`，schema41。
- 远仓：`https://github.com/GuanJHCode/Agent-Bird.git`。
- 接续分支：`feat/capability-closure-20261006`；克隆这个分支，不从远端旧 `main` 重做已完成能力。
- 最新已验证候选包：`0.1.0-closure.525d7d30fca9`，绑定 source `525d7d30fca936396c317272ee9485903cea8e1c`。源码推送不等于发布二进制或替换安装。
- 新一轮 retention-extensions / validation-exit-retry 只创建了本地隔离 worktree，尚无实现改动，也没有待收集的运行中实现 Agent。

```sh
git clone --branch feat/capability-closure-20261006 https://github.com/GuanJHCode/Agent-Bird.git
cd Agent-Bird/tools/orchestrator
go version
go build ./...
```

工具链要求 Go 1.26 或更新、Python 3、Git；Python 测试需要 pytest。现有 CI 使用 Go 1.26.x / Python 3.14，主要任务运行在 macOS。以上构建命令是新机器的基线检查，不是本文已经执行的 Linux 验收。

本次在源码基线 `dbb1a68` 上补做 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`，Go 1.26.8 darwin/arm64 交叉编译退出 0、无编译输出。实际使用已有工具链 `tasks/g0-macos/data/go/bin/go` 的绝对路径（当前 shell 的 PATH 没有 go）；新机器需要自行安装工具链。未运行 Linux 二进制，也未重复跑测试。

## 工作分工

| 工作 | 非 macOS 可推进部分 | 必须保留的验收边界 |
|---|---|---|
| 候选留存扩展 | Store 资格、引用与消费围栏、CLI/IPC、迁移、文档及定向测试 | Darwin maintenance actor、真实 Git/Host 清理链回 Mac 验证 |
| 自动重试扩展 | 策略、可信失败分类、冻结输入、有限重试与累计预算 | 已启动进程/有副作用阶段必须有原始终态与幂等性契约，不直接放宽准入 |
| 多 producer 联合返工 | DAG 闭包、输入向量、目标预约和事务实现 | 多 Provider 真实模型与原生候选链另验 |
| filtered Git 扩展 | 类型、准入、状态机及平台无关部分 | Seatbelt、Broker/carrier、C/R 转换和真实 Git 整合需 Mac |
| UNKNOWN 恢复 | 恢复协议、监督设计和持久状态 | 原 Host 消失不产生原 Wait 证明；不能用 PID absence 或 owner resolved 补造证明 |
| Codex Worker | 协议、Store/CLI、执行域方案和非平台代码 | macOS 嵌套 Seatbelt EPERM 尚未解决；公共入口继续关闭 |
| Linux 完整执行 | 可以在 Linux 原生开发 sandbox、进程/IPC、安装与公开执行接线 | 当前 Linux Provider/sandbox 公共路径仍关闭，collect-only 不代表完整执行已支持 |

推荐 Linux 作为后续主开发环境。Windows 可编辑代码；需要 Unix 进程、权限和 socket 的开发/验证宜放在 WSL2 或 Linux VM，并将仓库放在该环境的 Linux 文件系统。WSL2 是开发安排，不是已经验收的产品平台。原生 Windows 执行后端未支持。Intel Mac 的原生验收不能由 Linux 交叉编译替代。

macOS 沙箱/launchd、原生 Host/Provider、TUI/Hook、安装升级和 Darwin 资源行为仍需 Mac。现有 GitHub macOS CI 可做自动检查，但不代替真实登录会话/TUI验收。

## 已完成与剩余范围

已完成普通初始 producer/v2 compose 候选 TTL、Host terminal/progress 与 report 保留期、默认关闭的自动 GC、普通 validate/review 临时工作树清理、持久 Git 清理恢复、ACP Prompt 容量预留、Linux collect-only followup、有限 filtered 整合、有序 compose 和整合后 new-base rework。不要重复实现。

尚待完成：

1. Codex Worker 公共派发及完整读写执行链。
2. 已启动 validate 及 implement/compose/integrate 等有副作用阶段的安全自动重试。
3. 多 producer 联合返工。
4. filtered compose/rework/retry/continuation、LFS、自定义 hooks、签名。
5. continuation/rework/new-base/filtered 候选、历史未登记与不完整失败现场的可信登记和回收。
6. 原 Host 丢失后的存活执行树接管、UNKNOWN 现场可信恢复入口。
7. Linux 完整 Provider/sandbox 执行和 Windows 后端。

失败清理采用已确认规则：显式 resolved/abandoned 后按保留期回收，保留恢复索引；UNKNOWN 与无可信原始归属仍保留。不能把人工确认当作删除任意历史路径的权限。

## 验证与协作

- 用户要求功能优先、少做重复测试。每个独立变更只增加必要回归，复用未受影响的证据；最后补受影响的集成检查和独立审查，不默认全量重跑。
- 最多两个子 Agent，不递归。每个实现 Agent 独占隔离 worktree，共享契约先冻结，由主会话集成。不要让多个写入者或构建同时占用同一工作树。
- 新任务的脚本、计划、文档、数据放在 `tasks/<task-name>/{scripts,plans,docs,data,tmp}/`。
- Go 定向测试保留 `-count=1 -json` 原始输出，统计实际 PASS/FAIL/SKIP；并发/事务改动按范围补 `-race`。运行具体受影响测试，不使用全仓测试结果推断某个平台已经原生支持。
- 现有公开 CI 在 push 时触发 macOS 全套检查；本次源码推送不调整 CI 策略。

有效证据入口：

- [候选留存验收](../../candidate-retention/docs/validation.md)：18 个独立顶层测试与 18 个子测试的适用证据；native actor 使用 fake Store 回调与真实 child/Git/FS，不是单次真实 Store 全链。
- [validate 启动重试边界](../../validation-start-retry/docs/usage-and-limits.md)。
- [Linux 进程与 Coordinator 范围](../../linux-identity/docs/coordinator-native.md)。
- [支持矩阵](../../../docs/support-matrix.md)：其中旧包、Host 删除及共享 candidate TTL 的旧条目尚有陈旧文字，以后续任务验收和源码为准。

未解决的验收问题仍需保留：Formal60 RSS 窗口超限失败；`AGY source_host_start_failed` 观测尚未定位；真实 Provider/TUI/Hook、物理断电及更多平台组合未验。不能把这些写成全部通过。
