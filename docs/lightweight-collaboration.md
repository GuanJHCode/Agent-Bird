# 轻量协作入口

主脑仍由当前 CLI 担任。Agent Bird 提供本地任务运行时和可选的跨 CLI 执行入口，不需要 MCP 或额外的路由模型。自然语言、Skill 命令、计划与目标模式均可触发；是否调用由主脑结合原生规则、任务边界和已有授权决定。

## 调度原则

- 连续决策、共享状态多或交接成本高：主脑自行处理。
- 原生 subagent 能满足隔离、权限、预算和生命周期要求：可使用原生机制。
- 有独立验收标准，且用户希望节约当前 CLI 额度或指定另一 Provider：使用 Bird。
- 原生 subagent 与 Bird 的统一身份、并发计数和停止协议尚未验证，因此首版不跨两种机制同时启动。Bird 内继续使用既有并发限制。
- 主脑读取 Provider 状态并作最终选择；没有自动模型切换、额度探测或按价格排序。默认模型沿用既有“单次 > 当前 owner > 保存默认 > CLI 默认”规则。

Skill 只在宿主允许的工具、权限和委派规则内生效。它不能创建宿主不存在的角色、放宽权限、扩大授权预算或强迫原生 CLI 自动加载。

调度偏好通过当前 owner 的私有记录保存：

```sh
<package>/scripts/agent-bird routing status
<package>/scripts/agent-bird routing set --request <private-preferences.json>
```

请求文件权限为 `0600`，内容示例：

```json
{"version":1,"mode":"save-primary","preferred_providers":["grok","agy","claude"],"native_parallel":false}
```

`balanced` 为默认，`save-primary` 优先考虑适合且已启用的外部 CLI，`manual` 只在明确要求委派时使用 Bird。偏好由主脑解释，不是强制自动派发器；Provider 权限、预算和隔离由运行时执行。设置只影响当前 owner，不自动开启 CLI；`native_parallel=true` 被拒绝。

## 业务请求与恢复

`task dispatch --request <private-json>` 接收版本 1 业务请求。主脑创建权限为 `0600` 的文件，并提供：

| 字段 | 含义 |
|---|---|
| `request_id` | 当前 owner 内稳定的业务标识，调用失败时继续使用同一标识 |
| `provider`、`provider_lock` | 显式 Provider 与已确认的版本指纹文件 |
| `directory` | 实现任务的干净 Git 仓库根目录；只读任务的读取目录 |
| `prompt`、`acceptance` | 工作要求与非空验收条件数组 |
| `role` | `implementer` 或 `reviewer` |
| `paths` | 实现任务的精确仓库相对路径，不接受通配符 |
| `model` | 可省略；`cli-default` 明确使用该 CLI 默认模型 |
| `max_attempts`、`max_active_ms` | 已获授权的任务预算，不能由工具故障推导为新额度 |
| `grok_session_write` | Grok 任务需要已有授权后显式设为 `true` |

实现任务自动分配托管 worktree，保留原仓库的修改。脏源仓库拒绝提交；主脑应先在自己的隔离工作区整理可验收基线，不自动提交用户文件。

相同 owner、相同 ID、相同规范化请求返回原 handle；同 ID 不同请求返回冲突。写入准备检查点后才创建派发计划，RPC 前先持久化 control 路径。只有能够证明尚未提交的准备阶段才允许继续。已经提交但回执丢失时，只对账原任务，绝不重新派发。

- `task wait --handle <handle> --timeout-ms 30000`：有界事件等待；多任务 handle 同时指定 `--task-id`。等待超时不代表任务失败或需要重试。
- `task reconcile --handle <handle>`：核对原 coordinator、run 和任务集合，不重启 coordinator/Host，不消耗新模型预算。离线或证据不足时保留 handle 并报告阻塞。
- `task collect`、`task ack`、`task accept`：沿用既有协议。收取结果、确认收取、业务验收仍是不同动作。
- `task rework`：针对原候选按原协议返工，不通过创建新业务 ID 隐藏预算耗尽。

旧版缺失 control 路径的中断 handle 不能自动恢复；本次协议不会扫描别的 capability 或猜测任务归属。

## 四家入口与安装边界

开发者先构建现有便携运行时，再生成四个独立入口：

```sh
./scripts/build-plugin.sh 0.2.0-preview.1 ./dist/runtime
python3 tasks/lightweight-plugin-entry/scripts/build-native-entries.py \
  --runtime "$PWD/dist/runtime" --output "$PWD/dist/native-entries"
```

输出目录必须不存在。生成包自带运行时，用户运行不需要 Go/Python，也不需要每次指定 runtime 路径。构建工具会核对便携包文件摘要，拒绝符号链接和额外文件。每个包的 `scripts/agent-bird` 从自身位置定位运行时，支持安装后搬迁。

| 主脑 | 包目录 | 归属边界 |
|---|---|---|
| Codex | `codex-orchestrator` | 已验证的调用会话身份 |
| Claude | `claude` | 通过 `controller start --provider claude` 启动的受管进程 |
| Grok | `grok` | 通过 `controller start --provider grok` 启动的受管进程 |
| AGY | `agy` | 通过 `controller start --provider agy` 启动的受管进程 |

已验收的 Worker 为 Claude/Grok/AGY。本开发分支增加固定 Codex 0.154.0 的反向派发入口和原生约束校验，但首次真实编码未产生候选修改；尚未完成编码/审查/集成验收或发布。当前安装包和新版本 Codex 不能据此视为已支持。

非 Codex 主脑需要通过包内 `scripts/agent-bird controller start --provider <name>` 启动；安装 Skill 本身不产生可信 owner。进程内清空或新建原生对话不等于新 owner。停止 Provider 只作用于当前 owner 的委派。

当前交付是可构建源码和本地验证产物，未发布安装包。插件 manifest/脚本验证不能代替各家真实安装、自动 Skill 发现、原生交互与模型编码验收。未经相应验证，不宣称四家均可原生一键安装和全自动混合委派。
