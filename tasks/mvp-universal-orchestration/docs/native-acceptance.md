# Grok / AGY 单次原生编码验收

用户明确授权两种 CLI 各一次、每次最多 120 秒；无自动重试。两次额度均已消耗，均保留独立测试仓、worktree、Host 账本和 Provider 会话现场。未修改第三方认证、配置或权限策略，未升级现有运行时。

| CLI | 结果 | 测试耗时 | 实际证据 |
|---|---|---|---|
| Grok 1.0.34 | 失败 | 23.00 秒 | read_file 成功；search_replace 权限请求被 CLI 标记 cancelled；calc.py 未修改，Host failed 后记录 exited |
| AGY 1.2.5 | 通过 | 60.66 秒 | 隔离区内将减法改为加法，冻结非空候选；正数与负数两项行为断言通过，主测试仓文件和 HEAD 未变 |

AGY 候选为 `bd0d595f517f386177e056f26b3bb025c0fc5cdf`，基线为 `1c543573fad8723ae5822cbfabd38e151a6aad4c`。此通过范围为一次真实编码、冻结和行为检查，不包括原生主脑自动派发、复审、集成或其他版本/模型组合。

Grok 的 cancelled 是 CLI 的权限决策记录，不表示用户在本轮手动取消。已有会话日志还出现本机 hook socket 错误；读取文件仍成功，因此不把该旁证认定为编辑失败的根因。未修改这些已有 hook 或添加权限绕过参数。后续需核对编辑权限交互；未经新的明确授权不再次调用模型。

证据位于同任务私有 `data/`：`native-*-attempt.json`、`native-*-test.jsonl`、`native-*-01/spool/` 和 `native-acceptance-summary.json`、`native-grok-permission-events.json`（当前 Grok 会话原生事件的摘要及源文件 SHA-256）。原始会话和本机路径不进入公开文档。

验收辅助脚本补充 `check.Dir = work`，让行为检查也在隔离区执行；修改后两种模拟 Provider 的 Host 冻结回归测试通过，随后 AGY 原生验收通过。Grok 在该检查之前已失败，未因脚本调整重试。

独立只读复核确认两次额度、AGY 候选/行为断言和两主测试仓基线；Grok 权限原因由主会话核对本次原生事件。源 Skill 和支持矩阵已同步真实结果；使用同一已验证生产二进制重打包 `0.1.0-universal.2`，6 个文件摘要/权限通过，保留于 `tmp/package-native-acceptance`，未安装或发布。
