# Native activation fixture builder

`native_activation_fixture.py` 只准备一次 native activation case，不执行
native、model、`launchctl` 或 sandbox 命令。`build_fixture()` 返回 profile、
supervisor、manifest、pins、`ActivationSpec` 和 opaque `FixtureAuthGuard`，
供后续 probe 在明确准入后使用。

两种模式的 public endpoint 边界不同：

- `preflight` 使用 supervisor root 下的短路径 default socket，transaction
  spec 的 `home` 也是该 synthetic supervisor home；
- `real` 的 registration spec 指向当前用户的
  `~/.codex/app-server-control/app-server-control.sock` 和 LaunchAgents 路径，
  但 backend/profile 仍使用独立的新 `/private/tmp/g0-auth-*` profile root。

profile root 与 supervisor root 始终是两个新建、owner-only 的短目录。profile
只包含 synthetic Luna/medium/no-auth/file 配置与 trusted workspace；grants、
state、isolation manifest 和 activation manifest 都在 supervisor root，不在
native 可写 profile subtree 下。foreign、复用、symlink 或非 owner-only root
会拒绝，cleanup 只删除本 fixture 自己创建的 roots。

固定 manifest 字段包括：

- profile 的 `task_root/home/codex_home/workspace/public_socket/backend_socket`；
- 14 项 credential inventory 的 protected read/write path；
- native executable 与 SHA-256；
- activation service 的 backend argv、public/state/grants 路径；
- interpreter、entrypoint、activation service 和 launch activation source pins；
- 完整 `-I -B entrypoint --manifest` argv。

`FixtureAuthGuard.before/check/after` 只返回 inventory count、opaque digest 和
`unchanged`，不保存 credential 内容或完整路径清单。fixture 的准备阶段不调用
`ActivationTransaction.prepare/register/revoke`；root/probe 负责在准入后使用
返回的 `ActivationSpec`，并以 `managed()` 保证对称撤销。
