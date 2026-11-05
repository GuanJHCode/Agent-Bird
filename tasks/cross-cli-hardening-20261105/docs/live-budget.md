# 本轮真实验收授权与限额

2026-11-05 用户答复：采用已有账号/默认模型的有限真实验收，分钟数和模型均取最低可用档；Claude第三方API没有官方登录时沿用内置默认模型，不切换。

执行上限：每方向总计180秒、最多2次worker（初始+必要返工），worker单段最多60秒/同组总90秒，父会话与桥接也计入180秒场景总限。先核验本机已配置认证路由和精确CLI artifact，不读取/打印密钥、不新建账号/切网关。可独立选择模型时选已验证支持的最低档与最低推理；第三方CLI沿用既有默认模型，不强制官方别名。仅同一最终包冻结后串行执行两方向，不在实现/fixture阶段发模型请求。

超限、账户/模型不可用或原生能力不足，保留原ID/句柄/现场并明确BLOCKED；不自动换账号/模型、加预算或重置组。首轮全对如实记录happy-path及自然缺陷返工NOT_EXERCISED。上述是尝试上限，不是执行成功声明；当前真实验收NOT_RUN。

本机只读预检（2026-11-05）：固定Codex 0.166.0为ChatGPT登录/openai provider，当前可见模型cache含gpt-6-luna及low档；计划仅在最终包验收命令选择gpt-6-luna、low、default速度。全局配置不改。官方[Codex价格](https://learn.chatgpt.com/docs/pricing)与[模型入口](https://learn.chatgpt.com/docs/models)已实际打开核验；Standard credits不能当API价或订阅额度保证。本次未发模型请求，实际账号/模型可用性仍待live；不可用不自动升级。Claude第三方路由仍保持cli-default。预检摘要见data/public-evidence/live-model-preflight.json，不保存认证输出或密钥。

现有task dispatch未暴露reasoning，Codex adapter缺省为medium；最低档真实验收应使用已有task submit/run的typed ExecutionProfile(reasoning=low)，或在经审查的新入口明确支持后使用；不能声称缺省dispatch已满足low。
