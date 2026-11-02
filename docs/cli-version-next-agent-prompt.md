# 后续 Agent 接手提示词

```text
接手仓库 https://github.com/GuanJHCode/Agent-Bird.git，分支
handoff/cli-version-compatibility-20261102。
先记录实际 HEAD，阅读 docs/cli-version-handoff.md、
docs/cli-version-verification.json 和 docs/cli-version-source-manifest.json。

目标：完成 CLI 版本兼容剩余实现与验收。当前版本目录和 per-run pin 已落地；
不能把版本准入、fixture 通过或一次端点成功写成所有版本都能真实派发。
本分支是 Go 研发模块快照。公开 main/master 独立演进，保留其产品功能；
整合时选择性移植，不能直接整体合并快照、强推或删除公开产品入口。

优先处理三个已经定位的矩阵脚本缺口：
1. stopVerified 的 SIGKILL 升级前重新核验 birth、argv 和 PID 文件。
   先写 TERM 后 PID 被复用、身份查询失败等确定性反例；身份不明保留现场。
2. matrixDiagnose 对 capability/launch/provider-diagnostics 的采样
   必须绑定 Host 元数据的精确路径和身份，排除 scratch、artifacts 仿冒。
3. capability 诊断与生产 sandbox 的路径解析、capability id 约束一致，
   补合法符号链接前缀及跨 id/越界的正反例。

随后独立修复 internal/process 的 Stop/ExitCode 时序合同。
进程组消失不保证等待 goroutine 已发布退出码；-1 可能只是初值。
先确定性复现，明确 Stop 返回与 Wait/退出码可用之间的保证，
覆盖 Start 的 birth 失败、上下文截止、后代存活及管道尚未关闭路径。
不靠 sleep、放宽超时、删断言或跳过用例制造通过。

Codex 仍受 codex_trial_guard_not_ready 阻塞，四项 missing 是硬编码清单。
明确受信 metadata 的生产来源、refresh tripwire 的唯一注入方、
native 版本 admission 和真实 exec/collect 验收依赖。
不能仅删除门禁或缩短 missing 清单；同值 override 预置也会被 native guard 拒绝。

保留冻结稳定版目录（2026-11-02T01:53:00Z）、原始版本、路径和 SHA 校验。
不得修改全局 CLI 安装、认证、已有锁或扩大权限；不推送/发布后续修改，
除非本次接手任务另有明确授权。不要为了补数字反复消耗账户额度。

每批最小修改、补必要回归，在最后稳定提交上安排独立审查。
Go 1.26+ 下串行运行全量、race、vet、build，实际核对通过/失败/跳过数量。
本次已核验快照为 420 顶层/816 含子测试通过、0 失败、6 opt-in 跳过、14 包；
源码或环境改变后重验受影响部分。旧 AGY 真实通过对应旧包，不能替代新包。
更新隔离包和摘要链后，再按账户条件及授权补真实端点、中间版本和产品整合验收。

最终分别交付：实现完成项、自动化证据、真实 CLI 闭环、内部未完成项、
外部账户阻塞、未验证版本，以及可继续执行的下一步。原始私人运行现场不入公开仓库。
```
