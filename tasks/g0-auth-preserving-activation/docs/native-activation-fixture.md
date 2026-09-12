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

profile root 与 supervisor root 始终是两个新建、owner-only 的短目录。credential
inventory 必须由调用者提供一个新建、owner-only `0600` 的 14 行 path manifest；
builder 只解析注释/空行后的实际路径，不凭空推断 provider 或 AWS 路径。profile
只包含 synthetic Luna/medium/no-auth/file 配置与 trusted workspace；grants、
state、isolation manifest 和 activation manifest 都在 supervisor root，不在
native 可写 profile subtree 下。foreign、复用、symlink 或非 owner-only root
会拒绝，cleanup 只删除本 fixture 自己创建的 roots。

固定 manifest 字段包括：

- profile 的 `task_root/home/codex_home/workspace/public_socket/backend_socket`；
- real HOME 整根 write deny 与 manifest 中 14 项 credential 的精确 read deny；
- native executable 与 SHA-256；
- activation service 的 backend argv（包含 `app-server`）、public/state/grants 路径；
- 当前工作的绝对 interpreter、entrypoint、activation service、launch activation、
  `auth_isolation`、`proxy_transport`、`proxy_observer` source pins；
- 完整 `-I -B entrypoint --manifest` argv。

`FixtureAuthGuard.before/check/after` 只返回 inventory count、opaque digest 和
`unchanged`，不保存 credential 内容或完整路径清单。fixture 的准备阶段不调用
`ActivationTransaction.prepare/register/revoke`；root/probe 负责在准入后使用
返回的 `ActivationSpec`，并以 `managed()` 保证对称撤销。

成功 fixture 的 `cleanup_ephemeral()` 只清理 supervisor receipts/grants，保留
profile session；调用者完成对账后显式执行 `cleanup_profile()`。`cleanup()` 会
拒绝隐式删除。需要长期保留时先调用 `export_retained_session()` 写入安全摘要，
再由调用者决定 root 清理。
