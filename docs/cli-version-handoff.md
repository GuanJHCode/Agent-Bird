# CLI 多版本试用交接

## 当前交付（2026-11-03）

Codex 生产只读链路已实现；最终隔离包为 `0.1.11-cli-production`。8 个基线/上界端点和 2 个固定中间版本均在该包完成真实读取、marker 精确匹配、产物 SHA/size 校验、collect、ACK、accept、final completed。没有 package pin bypass 或合成 Provider。

| CLI | 当前包真实通过的版本 | 实际模型/路由 |
| --- | --- | --- |
| Claude Code | 2.1.267、2.1.290、2.1.316 | 用户提供的 Anthropic 网关，`deepseek-v4.1-flash-1017`，Bearer |
| Grok | 1.0.34、1.0.52、1.0.70 | 用户提供的 OpenAI chat_completions 网关，`deepseek-v4.1-flash-1017` |
| AGY | 1.2.3、1.4.12 | 现有原生登录，`gemini-3.8-flash-low` |
| Codex | 0.154.0、0.166.0 | 现有 ChatGPT 登录的只读投影，`gpt-5.6-luna` |

Claude/Grok 的结果证明原生 CLI 与此网关/DeepSeek 的兼容，不是第一方 Claude/xAI 模型验收。密钥、网关原地址、实际个人路径与原始会话只在仓库外 owner-only 环境；未修改全局配置、原认证、已有锁或冻结目录。

Go 最终全量和 race 均为 **441 顶层／856 含子测试通过、0 失败、6 显式跳过**，14 个测试包通过，vet/build 通过。Python 默认套件 **159 通过、4 跳过**，0.166 原生合成刷新测试另行 **1 通过**。0.154 不受影响的原生刷新合同与命名策略证据按组件摘要复用，分别为 13、1 通过。独立 Go、安全、0.166 永久测试及公开产品补丁审查均 PASS。最终包只补齐插件入口说明，Go/Python 执行字节与最终全量测试快照相同。

基线 `55741e28c783fc7b97eeba8e0688c31c0118f229` 初始干净，225 项摘要及远端一致；原研发脏 main 保留原样。当前提交以实际交接分支 HEAD 与[源码清单](cli-version-source-manifest.json)核对，不把历史基线当本轮结果。

## Codex 四项合同与生产证据

| 合同 | 生产实现 | 验收依据 |
| --- | --- | --- |
| 受信认证元数据来源 | `codex_exec.py:FileLoginSource/prepare_launch`；固定 native `login status`；来源和快照重复稳定读取、freshness、CoreFoundation preferences/配置层拒绝 | 缺失、非 owner-only、硬链接/符号链接、过期、来源变化、preferences 异常与其他认证路由均拒绝回归；当前包真实 ChatGPT 投影闭环 |
| refresh tripwire 唯一注入 | `auth_guard.py:prepare_guarded_environment`，由 installed helper 调用；Go Host 不设置 override | 任何已有 override（空值/相同值也包括）拒绝；0.154/0.166 固定 native 合成 positive/guarded 对照，guarded OAuth 计数不增，auth 字节与 identity 不变 |
| 精确 native 准入与工具权限 | `admit.go:CodexNativePins`、`verify_bundle`、installed receipt/helper/interpreter 校验；`render_native_config` 命名策略 | 两个版本 main+companion 固定 SHA；manifest/目录/文件权限、签名与篡改拒绝；实际 Seatbelt 工具探针验证控制目录/DB/兄弟 spool/原与投影 auth/config 拒读、workspace 拒写、own scratch/artifacts 可写、工具网络连接 0 |
| exec/collect 真实闭环 | `Host.ExecuteLaunch`、report wrapper pin 透传、installed native helper、JSONL observer 与 owner loop | 最终包两个 native 端点实际读 TOKEN，精确 marker、SHA/size、collect、ACK、accept、final completed；纯解析 fixture 不计原生验收 |

具体源码、SHA 和逐端点 round 指纹见[机器验收记录](cli-version-verification.json)与[脱敏端点证据](../tasks/cli-production-rollout/docs/real-endpoint-verification.json)。未知 native 身份仍保留完整四项 `codex_trial_guard_not_ready` 清单；没有缩短清单或删除 guard。

用户已明确批准：**固定摘要 native 主进程受信，由原生命名只读 profile 沙箱约束模型工具**。外层 Host Seatbelt 会阻止 native 工具再次启动 sandbox，旧嵌套路线已停止。CodeModeOnly 模型使用同样固定摘要的 companion，禁止 in-process fallback，delegate 工具仍走原生命名权限。写入、candidate、resume、任意配置/override 不能绕过该边界。

## 固定版本与账户

冻结时间 `2026-11-02T01:53:00Z`：Claude 2.1.267–2.1.316（46），AGY 1.2.3–1.4.12（35），Grok 1.0.34–1.0.70（37），Codex 0.154.0–0.166.0（21），共 139。运行时不解析 latest；可从官方固定 URL 隔离下载清单内版本。

[逐版清单](../tasks/cli-production-rollout/docs/version-inventory.json)分开记录版本准入、无模型、协议解析、真实模型：**13 个实际二进制无模型通过，126 未测；10 个真实模型通过，129 未测**。原始 `--version`、来源、路径别名、SHA、参数及拒绝原因逐版保存；实际个人路径与原始输出在私有记录中并以摘要绑定。Claude 2.1.290、Grok 1.0.52、Codex 0.159.2 为本轮从官方固定来源下载并核验；已有二进制摘要重验，不声称本轮重放了原始下载过程。

`ORCHESTRATOR_MATRIX_GROK_CEILING`、`ORCHESTRATOR_MATRIX_CODEX_CEILING` 连同其余端点已重新核对 raw version/SHA 后写入任务私有 0600 环境文件。公开的 `inventory_versions.py`、`configure_matrix.py` 不登录、不请求模型、不改全局安装。真实矩阵要求显式模型，缺失、空值、`cli-default` 或非法 ID 均拒绝。

- Claude 第一方登录检查为未登录；新网关 Bearer 路线已通过，不再以缺第一方登录阻断该试用路线。最初 x-api-key 401 现场保留。
- Grok 原账户无余额由用户确认，停止原账户模型重试；新网关路线独立通过。此前 150 秒超时不能被写成当前 402 证据。
- AGY metadata 与当前包两端点成功；未测量绝对剩余额度，不沿用旧包成功证明当前状态。

## 剩余范围

- Codex 0.159.2、0.165.0 只通过真实二进制/参数发现，缺精确 native 合同与模型闭环，完整四项门禁保持；其余 19 个 catalog Codex 版本均未获 native 准入。
- 126 个版本未做实际二进制验证；AGY 1.3.4 的已查官方 release 无 assets、bucket 列举 401，未取得可核验固定 artifact，不能猜测下载或标通过。
- 完整注册工具目录尚未单独持久化并逐项做 namespace allowlist 验收；已验证实际必需工具路径和 OS 权限。`cleanupCodexAuth` 路径检查/删除间的 TOCTOU 是当前 0700 Host-owned 路径下的后续加固项，未发现模型可达路径。
- typed resume、Codex workspace-write/candidate 保持不支持；native TUI 未验收。

## 公开产品整合

研发交接分支不能整体合入公开 main/master。[选择性补丁](../tasks/cli-production-rollout/docs/public-product.patch)基于公开 main `2e78f24c5e9694f287d19065f2ce9c80b16a59af`，27 文件，SHA `62cefc9a1902c9008e6d5e6a2ea418be0f0187fbaaf162f639470850d64aba0b`。版本目录/准入及 AGY cwd 兼容已实际移植到独立产品副本，保留 controller/dispatch/reconcile/coding/portable 入口；公开 Codex 仍守原门禁，没有移植研发 native guard。

[产品验收](../tasks/cli-production-rollout/docs/public-product-verification.json)：干净 base applycheck、独立审查通过；full 与最终 race 均 401 顶层／770 含子例通过、0 失败、4 跳过，vet/build 通过。首轮 race 时间窗失败与后续原断言三轮/完整重验日志均保留。**候选已实现和自动化验收，尚未发布，未做公开产品原生安装/模型验收；main/master 未更新。**

## 历史批次（已关闭，不是待修清单）

- `d7bc3d9`：历史 420／816、6 跳过；仅 Go 默认自动化。
- `613e3d0` 后续批次：矩阵 SIGKILL 前核验、精确诊断来源、capability 路径/id 约束、birth 失败及 Stop/Wait 合同已关闭；历史曾为 427／831。
- `55741e2`：process 调用方合同收尾，历史 430／834、6 跳过，225 项源码清单，旧包已重建推送。不是本轮修改后的通过数量。
- 本轮首全量暴露锁错误优先级及 Codex 解析 fixture 误入 native 门禁，均保留原断言修复并复核；最终 full/race 通过。0.166 合成测试的旧 5 秒读取窗口超时保留，仅该版本显式有界 20 秒后通过，严格协议/身份/计数断言未放宽。

沿用已授予的提交及交接分支非 force 推送授权；不操作 main/master，不逐轮请求审批。原始会话和私密材料不入公开仓库。后续以[任务记录](../tasks/cli-production-rollout/docs/progress.md)、当前源码与封闭验收日志为准。
