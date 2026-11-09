# T08：Claude 网关保留来源默认模型

状态：实现与独立源码审查 PASS，root 完成当前稳定源码验收。原 .4 包保持冻结；生产修复通过后构建新的唯一 .5 私有候选包。

## 问题与合同

第三方网关 worker 使用私有 HOME / ExactEnv。`cli-default` 的 ExecutionProfile.Model 为空，原路径只转交路由和认证，丢失调用方环境中的模型选择。即使 CC Switch 已连通，worker 也可能选用不同模型。

当 Model 为空时，仅按源环境原样保留六个模型选择字段：ANTHROPIC_MODEL、ANTHROPIC_DEFAULT_HAIKU_MODEL、ANTHROPIC_DEFAULT_SONNET_MODEL、ANTHROPIC_DEFAULT_OPUS_MODEL、ANTHROPIC_DEFAULT_FABLE_MODEL、ANTHROPIC_SMALL_FAST_MODEL。保留缺失和空值的区别，不填默认模型，不从用户 HOME 读取配置。显式 Model 的原四字段覆盖合同保持不变；不扩大 headers、betas、代理、动态加载器、hooks 或认证的继承范围。

## 归属与验证

主会话拥有隔离 worktree `t08-claude-source-model-20261109` 唯一写入权；独立审查只读，不派生子 Agent。修改范围为 host/claude.go、host/claude_model_environment_test.go、host/claude_native_test.go 与本计划。

TDD red 已保存到外部 evidence/data/red1.jsonl，证明原实现丢失来源选择。green1 已通过。最终针对稳定源码执行全模块 normal / race、vet / build，并用固定 Claude 2.1.316 对本地 SSE fixture 分别验证 API_KEY / AUTH_TOKEN、显式模型 / cli-default、只读 / managed-write。所有 native fixture 使用公开假凭据和 loopback，不调用真实模型，不构成双向真实验收。

最终测试计数、源码 SHA、独立审查与 root 验收会随提交补充。未通过前不提交；通过后立即 push。旧 .3 真实失败、.4 包与当前网关连通性证据独立保留。

## 最终 root 验收（2026-11-09）

固定 Go 1.26.10：normal / race 各1659pass（659顶层、1000子测试）、0fail、8既有opt-in skip；vet和全模块build exit0。22项 focused PASS；固定Claude2.1.316的APIkey/token loopback各6pass、无skip。最后一次相关源码修改后 hash 保持一致，集成3源码与验证快照逐字一致。

首轮全量 normal 保留1658pass/8skip/1fail：原有AGY能力测试在2秒版本探测窗口内返回binary_version_probe_failed，底层原因未序列化。源码、断言和超时均未改；同一AGY测试随后count5全部通过，fresh normal/race全量通过。不把第一次失败删除或归零。审查Agent review_claude_source_model只读PASS；root核验原始JSONL与实际测试计数后签收。

这里只关闭网关来源默认模型丢失的代码缺口，不宣称当前包/真实双向验收通过。生产变化要求新唯一 .5 私有包；.4及旧.3失败材料继续保留。
