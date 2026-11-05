# ADR-008：本地普通 MCP 工具

采用官方Go SDK1.8.0，仅stdio与2025兼容协议集合，不启Tasks/native push。输入frame2MiB，单次CLI捕获128KiB，完整MCP result（structured+text）256KiB；不截断事件或继续返回已截页的proof。4个在途call上限、每call最长45s，wait仍<=30s；取消/EOF仅取消调用，不自动stop/ack/accept/resubmit。

8工具dispatch/inspect/wait/collect/request_changes/accept/ack/stop。SDK负责协议、会话与取消；使用低层AddTool并由明确typed strict decoder（含精确字段名/嵌套字段）与原业务admission验证，避免generic schema map/float64重编码改变int64 ACK tuple。schema明确additionalProperties:false，未知owner/control/state参数拒绝。业务error用IsError并保留durable handle；协议错误由SDK处理。

configure只生成可信workspace/state/provider-lock路径、随机config namespace和私有32byte key，不绑定未来父CLI；serve在无owner时可list。key位于原worker禁止访问的StateDir/mcp-keys/namespace/token-key，不放在可写工作区；目录0700、key0600。新dispatch仍走taskEntry原currentOwner/ownerBind/worker排除/幂等checkpoint，路由签名来自其原private handle和reservation逻辑controller。route/review key按config持久化，不按物理PID或MCPsession；不建第二业务DB或决定ledger。读原handle/control/bootstrap每次核run/task/controller/runtime，再走原CLI/IPC；不缓存此前授权结果。

review_token绑定namespace/route/logical controller_thread/run/task/work_revision/event id+rev/hash/slot/segment/candidate。显式rebind只更换物理PID/birth，原logical controller不变；token不取代T07真实owner与事务snapshot。query/stop/ACK不新增owner-live门禁；managed mutation依旧要求当前persisted owner/ancestry。legacy native/helper保留已有bearer委派，不误称其具备managed ancestry隔离。跨config/key、跨route/task的引用拒绝，不提供任意control路径import接口。

request_changes从signed token恢复fully-bound请求，在RPC前写原CLI完全兼容的owner-decisions checkpoint，再复用原taskEntry；不得用fresh summary替换该tuple。CLI与MCP共享checkpoint路径与编码构造函数，原Store先receipt replay再新revision验证；兼容显式v2，默认v3，不能自动升级已v1/v2返工任务。两向跨入口同command重放原回执，改正文拒绝。dispatch同时返回cli_handle原路径（只读路由、非authority），MCP关闭后可用原wrapper沿此handle收尾；opaque handle仍供MCP使用。accept/ACK互相独立。stop区分requested（unknown可为null）和observed confirmed，不将unknown当已停止。

模型与预算必须显式提供给MCP dispatch；新增可选reasoning沿用原typed profile，省略时原CLI默认/编码不变。第三方cli-default不替换模型。Grok仅reviewer/read-only，必须显式grok_session_write=true；缺省/false拒绝，非Grok传true也拒绝，原profile门禁不变。MCP配置仅任务内，不写全局配置/审批。Codex stdio默认过滤环境，生成参数仅额外白名单AGENT_BIRD_OWNER_REQUEST变量名，不嵌入未来owner路径或密钥；无owner仍可list。worker empty MCP和原IPC worker ancestry拒绝保持。该环境行为有Codex0.166.0无模型metadata红绿实证，配置依据[官方MCP stdio文档](https://learn.chatgpt.com/docs/extend/mcp)。

SDK依赖将x/sys0.31升级0.41，需在合成/最终树重新验证T04文件回收、Host沙箱/进程与T06B事实/锁回归。最终安装/native/双向live另验，当前不以fixture冒充。

## T08 source auth environment 合同（替代上述仅 REQUEST 白名单）

可信 controller 在移除 CODEX_THREAD_ID 后、添加 AGENT_BIRD_* 前，对最终源环境生成可选 source_auth_env（version=1、algorithm=sha256-length-prefixed-source-auth-env-v1、sha256）。请求沿用私有0600文件，仅存摘要，不存源值。投影按最后出现值覆盖，变量名排序，以8字节大端长度前缀编码算法、逐个名字和值；unset与empty不同。投影涵盖所有 OPENAI_*/CODEX_*（仅排除 host/codex.go 已列出的8个非auth上下文字段，保留CODEX_HOME）、ANTHROPIC_*/CLAUDE_*、HOME、大小写HTTP/HTTPS/ALL/NO_PROXY，以及SSL_CERT_FILE/SSL_CERT_DIR/NODE_EXTRA_CA_CERTS。

每次 MCP dispatch 必须先验证 managed request 私有路径、launcher ancestry/birth/exe/hash，再验证证明版本/算法/格式及常量时间摘要相等。缺失或未知证明返回 mcp_source_environment_unverified；环境变化返回 mcp_source_environment_changed。生成的 Codex env_vars 为 configure 时当前投影的全部精确变量名及固定 AGENT_BIRD_OWNER_REQUEST，稳定排序，仅写名字。新增敏感变量未白名单时必须拒绝并重新 configure，不降级认证route，不自动切换model/account。原Host auth guard仍决定认证支持范围。无 owner 仍允许configure/serve/list；query/stop/ACK及旧token/receipt收尾不新增live gate；旧CLI路径允许缺省证明。私有dispatchMapped仅用于参数映射测试，未注册成RPC工具，不提供客户端绕过参数。

### 发布前 v1 投影集合补全（T08 r4）

上述 v1 尚未发布；其四 provider 源环境等价集合扩为原 OPENAI_*/CODEX_*/ANTHROPIC_*/CLAUDE_* 加 GROK_*/XAI_*、AGY_*/ANTIGRAVITY_*、GEMINI_*/GOOGLE_*/GCLOUD_*/GCP_*/VERTEX_*/CLOUD_ML_*、AWS_*、AZURE_*/FOUNDRY_*，另加精确 XDG_CONFIG_HOME/XDG_DATA_HOME/XDG_STATE_HOME/XDG_CACHE_HOME。保留原8个Codex非auth上下文排除，其余投影、编码、版本/算法校验、常量时间比较和fail-closed规则不变。

代码依据：host/grok_session.go 的 GROK_HOME（缺省回退HOME）选择既有认证/会话目录；adapter 的 ambient XAI_API_KEY 合同；host/claude.go 的 CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY route selector、CLAUDE_CONFIG_DIR/CLAUDE_SECURESTORAGE_CONFIG_DIR，以及其XDG配置目录。非gateway路径沿原 process.Start 合并 ambient 环境，故必须同时保留所选route的AWS credentials/profile/region、Google credentials/project/region、Azure/Foundry credentials/endpoint等配套环境，不能只转发selector。AGY/Gemini族与XDG位置亦纳入配置/账户路径等价集合。当前源码明确引用的必要exact route key均被该union覆盖；未读取真实账户内容或输出真实变量值，未以外部文档推导新支持的auth route。

configure仅转发当前存在的精确变量名字；未知suffix仍属对应族，其值仅参与hash。未来父环境新增相关变量而旧白名单未继承时，必须摘要不等并提示重新configure。该集合服务当前Codex/Claude/AGY/Grok四provider的既有父环境等价，不声明任意未来provider的全面auth保证，不扩大任何Host授权或provider支持、不自动选择model/account。真实controller→fake CLI→MCP stdio fixture使用公开sentinel证明各族missing/changed拒绝，完整集合仅验证到原provider_not_configured门禁；不调用真实provider/cloud。
