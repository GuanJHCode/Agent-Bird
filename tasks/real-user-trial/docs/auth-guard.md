# Codex 0.154 真实试用 auth guard

## 结论与边界

本模块为固定的 Codex `0.154.0` 提供一层试用 admission。它不启动 Codex，不读取真实 `auth.json` 或 Keychain，不调用模型，也不注册持久服务。真实 launcher 只有在可信边界补齐来源证明后，才能使用这里返回的子进程环境。

固定身份为：

- native version：`0.154.0`
- native SHA-256：`4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`
- refresh tripwire：`CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked`

固定源码没有 managed ChatGPT OAuth refresh 的正式禁用开关。实际变量是 `CODEX_REFRESH_TOKEN_URL_OVERRIDE`，它只替换 refresh endpoint。[固定 endpoint 与变量](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/manager.rs#L190-L203)、[refresh 请求](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/manager.rs#L1583-L1603)

Codex `0.154.0` 锁定 `reqwest 0.12.28`。该版本在执行请求前拒绝非 `http`/`https` scheme，因此上述 tripwire 不会解析 DNS、连接本地 listener 或把 refresh token 发给某个接收端。[reqwest 发送前检查](https://github.com/seanmonstar/reqwest/blob/v0.12.28/src/async_impl/client.rs#L2579-L2583)

这项保证限于固定 binary 和依赖。它保证 refresh token 不经 OAuth refresh 请求出网；它不保证旧 access token 一定被服务端接受。Codex 只在 access-token JWT 距过期不超过 5 分钟时主动 refresh，主动 refresh 失败后仍会尝试原 access token；若模型服务返回 `401`，恢复流程最终会进入 refresh 并在本地失败。[主动刷新条件](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/manager.rs#L2354-L2371)、[401 恢复](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/client.rs#L2325-L2553)

## Admission 接口

实现位于 `plugins/codex-orchestrator/lib/trial/auth_guard.py`：

1. 可信 native/SDK 边界实现 `VerifiedMetadataSource.read_verified_nonsecret_metadata()`。它只返回 `MetadataObservation`：固定 native version/SHA、`auth_kind`、`credential_store`、effective `use_agent_identity`、来源类别和证明摘要。不得包含 token、完整 config、认证文件内容或环境值。
2. `extract_safe_auth_metadata(source)` 校验固定 binary 和证明形状，并产生不可由 JSON/dict 直接替代的 `VerifiedAuthMetadata`。Python 对象不是跨进程能力；生产调用者必须硬编码可信 source implementation，不能让 worker、模型输出或请求 DTO 选择实现。
3. `prepare_guarded_environment(source_env, metadata)` 校验 admission 后复制正常来源环境，只在副本加入 refresh tripwire。它不清洗、不覆盖、不持久化来源环境。

若来源环境已经存在 `CODEX_REFRESH_TOKEN_URL_OVERRIDE`，无论值是否为空都返回 `refresh_override_already_present`，不会覆盖用户或企业设置。

允许的认证类别只有经证明的 `managed_chatgpt` 和 `api_key`。`unknown`、Workload Identity、command-backed external bearer、external ChatGPT token 均阻断。managed ChatGPT 的 store 只接受 `file/keyring/auto`；API key 只接受 `file/keyring/auto/environment`。这描述来源类别，不会切换用户认证方式。

effective `UseAgentIdentity` 必须为 `false`。固定版本默认关闭该功能，但最终值可能受 managed requirements 约束；仅凭默认值不足以放行。[默认关闭](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/features/src/lib.rs#L1750-L1755)

生产 launcher 可以给 trial 命令显式传 `-c features.use_agent_identity=false`，但仍须读取解析后的 effective 值。若 managed requirements 固定为 `true`，固定源码会把冲突的显式值作为配置错误拒绝，而不是让 CLI 覆盖管理要求；launcher 应把该错误映射为 blocked。[managed feature 冲突检查](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/config/managed_features.rs#L280-L319)

## 只读元数据候选

固定源码的 `codex login status` 在普通 managed/API-key 分支加载本地配置后调用 `AuthConfig.load_auth(enable_codex_api_key_env=false)`，不调用会主动刷新的 `AuthManager.auth()`，也不执行 token 持久化。[status 调用链](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/cli/src/login.rs#L443-L507)

它只能作为 auth-mode 证据的一部分：

- Workload Identity 分支会构造外部 `AuthManager`，所以必须先由可信内部元数据排除 WIF，不能用 status 自己做首次 WIF 探测。
- API-key 状态会在 stderr 输出经过遮盖的 key 前 8 字符和后 5 字符。`project_login_status(exit_code, stderr)` 只在内存中严格识别固定输出并返回类别；原始 stderr 不得进入模型、日志、错误信息或持久文件。
- status 不提供 credential store 或 effective `UseAgentIdentity`。生产实现仍需从已经解析的内部 `Config` 只投影这两个非秘密字段，并用 owner/native 证明绑定来源。
- `CODEX_API_KEY` env 在 status 中被明确禁用，不能靠 status 证明最终 exec 选择了 env API key。

因此当前模块没有把 `codex login status` 接成真实探针，也没有宣称 real-ready。它只提供安全投影函数和可信内部 metadata source 合同；真实 launcher/SDK 接线与来源能力验证由主执行边界完成。

## 固定源码对存储的影响

managed ChatGPT 的 `load_auth` 只调用所选 storage 的 `load`，不会在加载时把 file 自动迁入 Keychain。默认 `cli_auth_credentials_store` 是 `file`；`auto` 会先读 Keychain，再回退 file，但不会在读取时提升迁移。[默认 store](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/config/src/types.rs#L106-L119)、[Auto load](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/storage.rs#L431-L450)

只有 refresh 成功后才执行 `persist_tokens -> storage.save`。tripwire 在此之前失败，因此 refresh 路径不会改现有凭据。[refresh 后持久化](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/manager.rs#L3031-L3049)

API-key auth 原生不进入 ChatGPT OAuth refresh；但 `codex exec` 的启动 cloud-config 阶段会先以 `enable_codex_api_key_env=false` 加载持久认证，因此仅存在 `CODEX_API_KEY` 环境变量不能证明已有 ChatGPT 凭据不会被加载。`requires_openai_auth=false` 只决定 provider 请求是否附加 OpenAI auth，生产 session 仍会调用基础 `AuthManager`，也不能作为 refresh guard。

## 当前验证

`tools/real-user-trial/test_auth_guard.py` 只使用假环境、假凭据文本和固定非秘密证明 fixture，覆盖：

- 只修改返回的 child env，保留 `HOME`、`CODEX_HOME` 和其余来源环境；
- tripwire 不是 HTTP(S) endpoint；
- 已有 override 不被覆盖；
- binary/version/proof 不匹配、unsupported auth、unknown store、effective Agent Identity 均 fail closed；
- dict 自报的 auth mode 不能直接放行；
- metadata source 错误细节不会进入 block reason；
- login status 原始输出只投影非秘密类别，额外输出和失败退出均视为 unknown。

本阶段没有真实账号、真实 CLI、模型请求或 Keychain 访问证据，因此没有 real-ready 声明。
