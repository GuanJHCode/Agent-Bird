# 使用

先按 [安装说明](installation.md) 构建并安装插件，在目标项目新开 Codex 会话。

```text
$orchestrate 开启 claude
$orchestrate 开启 grok
$orchestrate 开启 agy
$orchestrate 状态
$orchestrate 关闭 grok
```

开启检查当前环境中的 CLI、版本、指纹和执行能力，随后启用本会话的派发资格。CLI 未安装或 profile 不支持时明确报错；探测成功不代表已登录。开启不预先启动模型，不自动授予执行预算。

关闭先阻止新派发，再取消当前会话对应 Provider 的待执行任务并请求停止其活动进程。`stopping` 表示仍在等待退出确认；`disabled` 才表示该 Provider 在本会话中已关闭且没有活动执行树。其他会话和自己打开的 CLI 不受影响。关闭可能阻断同一 DAG 的依赖任务，重新开启不会重启历史任务。

## 委派

```text
用 Claude 实现这个功能，完成测试、审查后集成。
用两个 Grok 分别只读分析模块 A 和模块 B，最后汇总。
用 AGY 只读检查这个目录，给出源码依据。
```

Codex 自动准备任务目标、文件范围、输入版本、执行者和验收条件。写任务必须使用独立托管 worktree，测试和构建在候选验证区执行，无需每次重复要求。已有未提交修改不会被自动提交或丢弃；相关未提交输入需先明确纳入候选基线。

每个任务保留独立 handle、执行预算和证据。超时或中断后继续使用原 handle，不自动加预算、不另建任务绕过失败。

## 模型

```text
$orchestrate 将 claude 当前会话模型设为 <model-id>
$orchestrate 保存 grok 默认模型为 <model-id>
$orchestrate 将 agy 当前会话模型设为 CLI 默认
用 Claude 的 <model-id> 模型分析这个问题。
```

优先级是单次任务 > 当前会话 > 保存的 Provider 默认值 > CLI 默认值。CLI 默认表示不传 `--model`。已提交任务的模型选择保持不变；不支持所选模型时明确返回错误，不自动替换。配置保存在编排器私有状态中，不改第三方 CLI 配置。
