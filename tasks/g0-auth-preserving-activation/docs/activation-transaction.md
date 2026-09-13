# Activation transaction controller

`scripts/activation_transaction.py` 只覆盖一次用户级 activation 的文件事务和 fake
`launchctl` 离线合同；它不会调用真实 `launchctl`，不会创建 public UDS，
也不会启动 native/model。入口由 sd07 worker 提供，当前约定如下：

```text
ProgramArguments = [
  <absolute Python interpreter>, "-I", "-B",
  <absolute projectproxy_launchd_entrypoint.py>,
  "--manifest", <absolute service-manifest.json>
]
```

`ActivationSpec.program_arguments` 必须是冻结后的完整 argv。job 验证只接受
`label`、`path`、`program_arguments` 以及可选的 PID 元数据；不会把
`launchctl print` 的原始输出或环境持久化。manifest 与解释器/entrypoint
hash 通过 `manifest_sha256`、`artifact_hashes` 固定，receipt 只保存路径和摘要。

事务顺序是：

1. `prepare` 先查询既有 job label，再检查 fakeHOME、owner-only socket parent、
   LaunchAgents、manifest、artifact hash，以及 public socket、plist、lease 是否
   已经存在；任何外来对象都拒绝接管，且既有 job 时不写 stage/lease、不执行
   bootstrap。文件 identity 使用 `O_NOFOLLOW|O_NONBLOCK`、`fstat` 和有界稳定
   读取。通过后用 `O_EXCL` 写入 mode `0600` stage 和 lease，并分别 `fsync`
   文件及父目录。
2. `register` 用 `link(2)` 将 stage 发布为 final plist。`link` 的
   `EEXIST` 是拒绝结果，代码不使用普通 `rename` 或 `install`。发布后再次
   核对 dev/ino/size/hash，随后才执行 fake `bootstrap`，并用安全字段查询
   job 的 label/path/完整 argv。
3. bootstrap 返回失败时必须再次查询：确认是本事务 job 才允许 bootout；
   确认是外来 job 时只删除本事务孤儿 plist，绝不 bootout 外来 job；查询
   不确定时进入 `UNKNOWN`，保留 plist/lease 等证据。
4. `revoke` 在 bootout 前先复核 final plist 的 dev/ino/size/hash/uid/mode，
   只对精确匹配的本事务 job 调用 bootout；bootout 后再次确认 job absent，
   且 final plist 仍与登记 identity 相同才删除。残留 public socket 先在
   冻结 parent 下原子移入本事务随机 mode-0700 quarantine，再用打开后的
   entry identity 判断：匹配的旧 socket 才从 quarantine 删除。若是目录项
   置换对象，则用 no-replace hard link 恢复；恢复遇到 `EEXIST` 时保留新
   public object 与 quarantine object，receipt 记录 quarantine path 并进入
   `UNKNOWN`，绝不覆盖或删除新对象。该隔离发生在 job 已确认 absent 后，
   因此正常 listener 不会被中途移走；删除旧 quarantine entry 后还要复核
   public name，若新对象已出现则保留它并进入 `UNKNOWN`。无法证明时仍需
   人工 owner 审计。
   lease 只在自身 identity 仍匹配时删除。

状态为 `NEW -> STAGED -> PUBLISHED -> BOOTSTRAPPING -> REGISTERED ->
VERIFIED -> REVOKED`；bootstrap/查询/身份竞态无法判定时为 `UNKNOWN`，不
自动重试。`managed()` context manager 对正常退出和 body exception 都尝试
对称 revoke。auth guard 在 prepare 前建立 baseline、publish 前复核、成功
撤销或已知回滚后最终复核；发生变化只报 `auth_changed`，不恢复旧备份。

测试使用 `/private/tmp/g0-auth-*` 短路径，避免 macOS UDS 104-byte 限制；
fake launchctl 通过 subprocess 接收真实 argv，并注入 bootstrap 失败、job
已创建、外来 job、foreign plist/socket/lease、publish 前漂移、稳定文本
`launchctl print` 解析和撤销替换竞态。测试不读取用户真实 auth，不写全局
配置，不执行 native/model。

尚未包含的边界由 entrypoint/guard worker 负责：继承 `launch_activate_socket`
FD、普通 Codex probe 的 peer admission、fork/exec 后实际 PID/birth/UID 与
expected executable/profile lease 的核验，以及 private backend 生命周期。
