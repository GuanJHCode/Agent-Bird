# sd03 关系与 session 准入

按主合同 `2bdd04e` 的新独立 case 登记，运行 helper 只允许 `ACTIVE_NONCE = "sd-qual-03"`。该常量没有环境/CLI 覆盖入口；原 sd01/sd02 不重跑。03 尚未执行或获原生资格，本提交只落实必要读取门槛。

原有 prepare 的同服务完整 `thread/read` 继续精确核 root、cwd、source=vscode、固定版本，以及 full/idle/READY/全部工具终态。所有后续完整读取都新增以下约束：parentThreadId 与 forkedFromId 两个键必须实际存在且值为 null，ephemeral 必须为 false，sessionId 必须是规范小写带连字符 UUID。不能用 get 的缺省 null 代替存在性检查。固定 [Thread 序列化源码](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs#L200) 中 Option 关系字段没有 skip_serializing_if；schema 的非 required 不意味着本资格允许缺失。真实缺字段仍拒绝。

prepare-meta.json 新增 `session_id`，值来自首次通过验证的原生 sessionId；prior_facts 同时保留 session_id 与 fresh_root_family=true。send-once 加载同一准备记录并于唯一发送意图前对比原生 session；其内部观察及显式 observe 每次读取也对比该值。UUID 可以与 root 不同。旧 meta 缺 session、无效 UUID 或不同 session 均不继续；不会通过新窗口/第二次发送补救。此次没有新增 RPC、binding caller 字段或业务事务。

三个回归先取得 RED：新03被旧 nonce 限制拒绝；缺关系/非空关系/缺失或非法 session 被旧过滤器误接受（ephemeral 原检查保留）；准备记录缺 session。最小实现后，覆盖不同于 root 的合法 session、prepare 的持久字段、发送前换 session 不创建意图、发送后及显式 observe 换 session 保持 uncertain、缺旧 meta 拒绝，全部与既有回归共 33 项通过（0.157s）。日志为 sd03-nonce-red、sd03-family-red、sd03-session-red 与 sd03-green；没有修改系统时钟或启动原生/模型。

历史 sd02 整链仍实际调用 collector/controller binding 和固定 Go read/inspect 的私有副本。测试内部显式 patch ACTIVE_NONCE=sd-qual-02；原 binding、controller-evidence 与五份 Go 文件，以及已保存 fixture 原始字节均不修改。原新服务投影并未记录 family/session，故测试额外提供 null/null 与固定 UUID，连同先前 notLoaded→idle、死亡 PID/libproc/socket/RPC 替身一起明确标为 synthetic。这个通过结果不能反称旧原生响应已观察到这些字段，也不能作为03原生阳性。
