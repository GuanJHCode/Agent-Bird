# CLI 版本兼容交接快照（2026-11-02）

## 快照范围

本分支从公开仓库 main 的 `2e78f24c5e9694f287d19065f2ce9c80b16a59af` 建立。
它包含研发树的完整 `tools/orchestrator/` 模块和 `tasks/g1-g4-delivery/scripts/package-plugin.sh`，不导入研发仓库的 Git 历史。
模块路径仍是研发模块原有的 `codex-cli-orchestration-design/tools/orchestrator`；独立 Go 构建不依赖仓库托管地址。
其余公开产品文件保留基线版本，不能把控制面测试结果解释为公开产品插件、安装包或默认 master 已整合。

本地源码捕获时逐文件复读确认未变化。矩阵测试文件 SHA-256：
`74d8f1483a040b6f6e5a70413a5f6d37d19d10879a31e9dc89b073e6c4530da2`。
其他导入文件摘要见 `cli-version-source-manifest.json`，只记录仓库相对路径。

## 已落地

- 已发布稳定版目录：Claude 2.1.267–2.1.316（46 个）、AGY 1.2.3–1.4.12（35 个）、Grok 1.0.34–1.0.70（37 个）、Codex 0.154.0–0.166.0（21 个）。冻结时间 2026-11-02T01:53:00Z；运行时不联网扩窗。
- 每次运行仍绑定实际二进制路径、原始版本原文和 SHA-256；预发布、未发布缺口与越界版本拒绝。
- missing PID + absent PID file 已进入 holder 检查；产物要求完整内容等于本轮 marker 或 marker 加一个换行，并校验摘要、大小及文件形态。
- Host 符号链接路径比较、同 launch 的 capability id 绑定、安装文件模式受 umask 影响、AGY 字符串 event 的诊断归类均已有实现与回归。
- 当前源码还包含跨 Host FIFO / 唤醒、停止生命周期、实测预算、非零退出与 artifact accept 约束等研发树既有增量。

## 独立只读复核

当前矩阵、sandbox、诊断与安装增量，以及上述 Host / Coordinator / Store 增量分别经过独立只读审查。后者未确认有充分证据的高风险回归；这不代替运行验证。

本次仍确认三项矩阵脚本问题，留给接手者修复，本分支不启用真实矩阵验收：

1. `matrixCoordinator.stopVerified` 在 TERM 等待超时后直接 SIGKILL，未重新核验 birth、argv 与 PID 文件。等待期间发生 PID 复用时可能误杀。先补确定性负向用例，再在每次升级信号前复核身份；未知身份保留现场。
2. `matrixDiagnose` 的事件文件已有路径和 run/task 过滤，但 capability / launch / provider-diagnostics 仍按 basename 全树采样，provider 可写目录能污染诊断来源。分别约束 Host 元数据的精确路径及身份。
3. capability 诊断仍比较原始路径与解析后的 reports，未跟随生产守卫的路径解析及 capability id 检查。合法符号链接前缀会出现诊断误拒绝。同步行为并补正反例。

holder 扫描仍只能看到 argv 带用例路径的进程；不能声称所有 Provider 子进程都已被证明退出。

## 仍未完成

- `internal/process` 的 Stop / ExitCode 时序：Stop 以进程组消失作为返回条件，而等待 goroutine 可能尚未发布退出码。研发环境的受控实验报告已复现初始值 -1；本次只读代码复核确认该窗口仍存在，未修复。测试的停止窗口是 3s，trap 的清理耗时才是 0.3s。需要确定性回归、明确返回契约，并覆盖 Start 的 birth 失败及管道未关闭路径；不要用 sleep 或放宽超时掩盖。
- Codex 仍在启动前返回 `codex_trial_guard_not_ready`。四项 missing 是硬编码清单：受信认证元数据来源、refresh tripwire 的生产接线、native 0.154.0 准入边界、真实 exec/collect 闭环。fixture 不代表真实启动。guard 输入环境预置 override（即使同值）会被拒；后续设计须明确唯一注入方，不能重复注入或删除门禁代替实现。
- 历史 AGY 两端点有真实闭环记录；它们对应旧的隔离包，本分支没有新的真实模型验收。两次 AGY 1.2.3 阻塞已记录为 active 预算到期；为何变慢仍未定位。
- Claude 登录态、Grok 余额及 AGY 配额是历史账户阻塞，当前未重新探测。所有中间发布版未逐版实测。
- 公开产品入口与本模块存在独立演进，需要专门集成，不能将本交接分支直接当作 main/master 的可发布升级。

## 验收

本快照将从提交后的干净独立检出执行默认 Go 全量、race、vet 与 build。运行完成后另附绑定候选提交的结果；此前研发树的 408/773、420/816 都只作为历史结果，不冒充本快照验收。

## 接手顺序

先修复三项矩阵脚本缺口，再独立处理 Stop / ExitCode 合同。保持版本目录、pin、摘要校验和现有权限门禁。基于稳定提交复核并测试，然后更新隔离包与摘要链；账户可用且获得真实执行授权后，再补端点闭环与中间版本验证。公开产品集成另列清单，保留远仓已存在的功能。最终分别报告版本准入、自动化、真实 CLI 闭环、外部账户阻塞及未验证范围。
