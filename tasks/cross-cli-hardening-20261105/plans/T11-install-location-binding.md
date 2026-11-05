# T11 installed manifest 位置绑定

真实候选 .1 的同文件系统 rename 保留全部 inode，doctor 仍 healthy，违反 PKG-03 和本轮安装说明。旧包与日志保留历史，不能收窄 move 为 copy 制造通过。

新安装写 installed manifest schema2，加 installed_root=规范绝对版本目录；原 receipt 仍两字段，以完整 manifest SHA 绑定新字段。读取 legacy schema1（无位置字段）保留原合同，不隐式迁移旧收据。schema2 缺失/relative/unclean/不匹配位置拒绝；Doctor 给重新安装原因，Pin/CodexGuard/native bridge/entry plan不自动采用移动后的布局。package-manifest/runtime-manifest/SQLite/外层uninstall-intent版本不变。

所有 installed manifest 消费者同步：install 共用读取/Pin/intent；Go nativebridgecli strict DTO；packaged native-product-bridge.py；native_product_plan.py。卸载 intent 内嵌v2的 installed_root 与逻辑 versions/version 比较，允许本轮已有卸载 quarantine 物理rename继续恢复；不可把任意移动目录冒作quarantine。保留原FD/inode/已知文件安全删除协议。

root唯一写本隔离树。先真实rename新安装与兼容/格式/intent红测试，再最小修复，normal/race/Python相关、新独审及root集成签收后commit/push；由新clean source构建唯一 .2 私有候选，原 .1 不覆盖或当最终包。无模型/账号/全局配置变更。

恢复 intent 的 typed manifest 必须与原 receipt SHA 所覆盖的原始 manifest bytes 一致，不能编辑 installed_root 绕过绑定。v2 intent 保存原始 bytes；历史无 bytes 的 v1 intent 通过原安装器的精确 JSON 表示重建并核对 SHA，不能降级 v2。任何仍有待删除文件的恢复，都先验证原 receipt 的 version/hash，再执行原 FD 删除协议；防止重写快照/hash 后直到删除完文件才发现 receipt 不匹配。保留完整历史红测与真实 .1 失败证据。
