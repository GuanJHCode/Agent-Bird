# 源码提交与 Codex 插件核对

用户授权提交并推送当前代码到 Agent-Bird，然后检查 Codex 是否直接使用
最新插件。沿用隔离分支 `feat/four-cli-delegation` 和原远端，不修改 main，
不发布 Release，不切换宿主插件配置或重启协调器。

本批包含：Codex 轻量读改 Worker、Grok 历史/当前版本兼容，以及主脑前台
等待、核验、验收与展示流程。保护已有代码、运行时、会话、证据和预算。
root 是唯一写入者，独立审查只读检查公开范围、敏感信息及说明一致性。

## 验证

- 在提交候选上运行 `go test -race -count=1 ./...`：1,059 pass、15 个
  native opt-in skip、0 fail，17 个测试包通过。
- `go build ./...` 和 `go vet ./...` 通过；前一轮 10 项生成入口/安装
  unittest 与 Plugin/Skill 校验仍适用，相关实现没有再次变化。
- 发布审查发现业务会话交接混入候选，已将两份本地 handoff 排除提交，
  并泛化公开进度中的业务仓、任务路径和授权状态。原文备份、业务交接、
  安装包和运行证据保留本机，不发布。
- 修正 CLI README 与轻量协作文档中已经过时的 Codex 准入描述；没有
  修改执行权限、Provider 配置或测试断言。

测试、现场观测与发布回执在私有且 Git 忽略的 `data/` 中保存；较早任务
记录中“未提交/未推送”等表述描述其当时检查点，不是当前远端状态。

## Codex 实际入口

当前 Codex CLI 为 0.156.0。原生列表显示 `agent-bird@agent-bird` 已启用，
版本仍为 `0.2.0-preview.4`；旧 `codex-orchestrator@codex-bird` 已停用。
preview.4 Skill 没有新增的前台收尾指令。

新版 `0.2.0-preview.5-result.1` 运行时已并存安装，包含新版协议，但未成为
原生默认入口。同一 Grok 1.0.40 的公共 probe：默认插件返回
`grok_version_not_verified`，新版返回 `profile_supported=true`。这只是
非生成能力检查，不代表主脑/Worker 的真实模型闭环验收。

因此当前普通 Skill 调用不会自动使用新版。已有 owner 可按已安装新版
协议选择版本固定入口，继续原句柄；直接使用最新版原生插件仍需完成
保留旧会话与缓存的安全更新。Git push 不会更新本机插件缓存。

本轮没有更换默认插件、修改宿主配置、重新派发业务任务或新增模型预算。
