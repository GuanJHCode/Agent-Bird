# 下一次用户级注册/撤销验证计划

本页是只读计划，不是执行授权。下一次用户级注册、撤销或永久安装必须取得新的明确授权；当前不运行 `launchctl`、native 或模型。

## 入口与范围

复用 [`ActivationTransaction`](../../g0-auth-preserving-activation/scripts/activation_transaction.py) 的 prepare/register/revoke/managed 入口，以及当前 `ActivationService` manifest/lease/receipt 合同。使用一次全新的短生命周期 case 和全新的隔离 HOME、CODEX_HOME、workspace、supervisor root 与 private backend socket。不得接管旧 lease、旧 public socket、旧 profile 或旧账号配置。

## 必要保护

- 14 条已登记 auth 路径只做 opaque metadata/hash 前后快照；禁止复制、解析、恢复、登录、登出、切换账号、Keychain 读取或真实 auth 写入。
- fresh profile 的 auth fixture 保持 absent 或纯合成；clean environment 明确列出 HOME/CODEX_HOME/PATH 等变量，不继承账号/token/API key 环境。
- 使用一个临时用户级 job，固定绝对 argv、artifact/source SHA、owner-only lease 和身份冻结；不设置永久安装语义，不启用 RunAtLoad/KeepAlive。
- 注册后只接受登记 PID/UID/birth/executable/profile；撤销前重新核对 job、plist、parent/socket identity。任何对象置换或状态不确定都保留 UNKNOWN，不自动删除外来对象。
- 测试退出顺序为 owner/frontend close、helper/backend EOF 和 guardian/reaper receipt 完成，再执行对称 revoke；public socket 的所有权和残留必须按冻结 inode 处理。

## 通过门槛

必须同时获得：单一自有 job、private backend/owner-helper 组合、明确 ready/history/ACK 证据、`auth.unchanged=true`、无外部模型调用、完整 cleanup 和 revoke receipt。普通 TUI、真实模型、持久 history 或完整 G0 仍需各自证据，不能由一次注册本身推导。
