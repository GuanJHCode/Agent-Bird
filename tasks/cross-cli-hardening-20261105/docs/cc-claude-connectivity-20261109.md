# CC 当前 Claude 配置连通性验证（2026-11-09）

**结果：BLOCKED，CLI报告API Error HTTP403。** 当前配置已生效并确实用于调用，不再使用此前聊天里的旧Key/API。

root核对`~/.claude/settings.json`与CC Switch当前Claude provider的完整配置一致；仅内存提取ANTHROPIC环境字段，使用原ANTHROPIC_AUTH_TOKEN/Bearer、原默认模型及映射。地址为`https://dashscope.aliyuncs.com/apps/anthropic`。没有将token转换为API-key，没有切模型/账户或改全局设置。

单次固定Claude2.1.316（SHA64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f），私有HOME/config/CWD，禁工具/skills、strict MCP无server、无session持久化、user/project/local setting sources为空并disableAllHooks；不使用不适合token认证的bare模式。两个标准managed policy文件检查不存在，不宣称绕过或禁用所有系统策略。普通重试0、单turn、输出上限128、请求期限30秒加5秒收尾预留，未继承额外gateway discovery开关。

实际CLI exit1，0.41秒，JSON可解析，API错误码403；未得到模型回答、nonce未匹配。CLI报告input/output/cache tokens均0；未超时、无清理错误、未发现owned CWD残留，密钥内容扫描0命中。没有连续重试。原始stdout/stderr不落盘，只保留白名单状态/计数；账单未核验，不能把CLI usage0当成服务商未收费保证。

[阿里云Claude Code接入说明](https://help.aliyun.com/en/model-studio/claude-code)区分按量、Coding Plan及Token Plan入口；当前域名是官方按量Anthropic入口。[官方错误码说明](https://help.aliyun.com/zh/model-studio/error-code)列出多种403模型/应用/业务空间权限拒绝场景。这里只读核对文档，单凭403不能确定具体原因；未自动改接口/Key/模型或开通权限。未保存原始错误正文，具体服务端错误类别尚未还原。

这是一项用户新授权的独立Claude连通性验证，不是`.4`产品双向live。此前旧路由401和首次UNKNOWN保持原证据，不重置旧预算；新`.4`真实派发、父审查、accept/ACK/整合仍NOT_RUN，core/T12/T13/native push仍BLOCKED。公开数据见[本次摘要](../data/public-evidence/T12-cc-claude-check-20261109.json)。
