# Claude 既有 Key/API 连通性复测（2026-11-09）

结果：**BLOCKED**。这是用户在402af7cc交接后新授权的最小独立Claude CLI连通性验证，不是`.4`双向产品闭环，也不把原`.3`预算或失败记录重置。未修改生产代码、安装包、登录或全局配置。

当前常规Claude settings及CC Switch当前Claude条目未找到生效Key/API；使用的是用户此前在本会话明确提供的Key、API和默认模型，仅root内存加载并进入精确子进程环境。没有改用其他账户、备用key或不同默认模型；没有在代码、argv或报告保存密钥。

两次CLI进程均限制单turn、普通重试0、最多128输出token、25秒请求期限和5秒清理预留，禁内置工具/MCP/skills/session持久化，私有HOME/CWD与写入范围隔离：

1. 首次进程启动后，收尾脚本在killpg阶段报PermissionError，未可靠保存回包；API结果UNKNOWN，不能当作没有请求、成功或401。原marker/脚本和诊断保留。核对私有CWD后未发现残留进程，密钥落盘扫描0命中。
2. 修复为communicate等待自然退出，先保存白名单元数据、仅超时时清理；5个纯本机夹具通过。只补测一次，实际CLI exit1，0.564s，返回JSON含API Error HTTP401；nonce未匹配，CLI输入/输出/cache token统计均0。没有超时、清理错误或残留owned进程，未继续重试。

认证方式为原来源ANTHROPIC_API_KEY，不猜测Bearer/token替换。401不证明key失效、模型不存在或第三方是否扣费；token0仅是补测CLI报告值，第三方账单未核验，首次结果未知。原始stdout/stderr未落盘，报告仅保留HTTP错误码、计数、耗时等白名单；两次扫描均未发现凭据落盘。

[公开摘要](../data/public-evidence/T12-claude-recheck-20261109.json)包含外部15文件证据索引及hash；本轮结果已通过[独立审查](../reviews/T12-claude-recheck-review.md)和root核对，仅为准确的BLOCKED记录。历史私有`.3`两方向失败、`.4`已通过的源码/PKG/离线回归保持原身份；新`.4`真实父/worker审查、accept/ACK/整合仍NOT_RUN，core/native push仍BLOCKED。

用户现称Claude已有自己的Key/API；如果指另一份已配置路由，应只提供配置文件、启动脚本路径或管理工具名称，不发送密钥。定位可用路由之前不再重复同一配置的调用。
