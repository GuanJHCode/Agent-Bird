# 当前恢复入口（2026-11-09）

先读[完整交接](final-handoff.md)和[TASKS](../TASKS.json)。当前资料已最终独审/root签收为PREPARED；产品core/T12/T13、T09 native push仍BLOCKED，新.4真实模型试验NOT_RUN。

工作分支`feat/cross-cli-hardening-20261105`，集成目录`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration`。最新生产功能已独审/root验收并push7b7f0421；新文档提交不改变私有包源7b。原工作区及未跟踪文件未动，未merge正式分支/release/替换全局安装。

私有`.4` archive SHA256 `1817a713d710ac0d6c75539b1c765b5826f991d2ab9b81befd78b47b42d33597`。PKG/独审/root、Go1654normal+1654race（各8skip）、Python301pass/7skip和两CLI无网络8工具metadata见交接证据。首build失败已保留，同源受限attempt2成功，package仅1次；禁止删除marker或第三次build。

原`.3`两方向模型试验失败并已收尾；Codex长引用问题已离线修复成route79/review47，新模型传递未验；Claude10次401且用户无可用本机配置。不猜认证模式、不索取或打印密钥、不自动追加付费CLI验收试验/重置旧预算/切账户模型。只有可用既有认证路由及明确新受控验收授权后，才准备后续真实试验。

本轮Go1.26.10与pytest运行时已恢复到任务专用稳定目录，准确路径和provenance见T08 root证据。保留全部私有包/失败现场/authority与0700/0600权限，勿运行reset/clean/gc/prune或全局清理。[历史恢复记录](resume-history-20261105-20261109.md)仅作过程证据，不用其中旧“当前”覆盖本文件。

最新后续：用户在402af7cc交接后授权Claude既有Key/API最小连通性尝试。此前会话同配置补测HTTP401，CLI token0；首次采集异常/API结果UNKNOWN。共两次CLI启动，已收尾且不再自动重试。见[复测记录](claude-connectivity-20261109.md)，独立审查/root已通过准确的受阻记录，认证本身未通过。若另有已配置路由，只需路径/管理工具信息，不提供密钥；新.4真实双向仍NOT_RUN。

最新CC配置验证：用户随后已完成CC Switch Claude配置，root确认生效settings与当前provider一致，使用新AUTH_TOKEN/原默认模型调用一次，CLI返回HTTP403、usage0、无模型回答/残留/密钥落盘。见[当前CC验证](cc-claude-connectivity-20261109.md)。这次不是旧会话Key/API，也不再以“未找到配置”描述当前状态；先前401记录仅为历史。未自动修改接口/模型/权限，`.4`双向仍NOT_RUN。
