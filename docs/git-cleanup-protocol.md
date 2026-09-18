# v0.1 Git、清理与安装生命周期协议

状态：待实现的合同；已完成的少量 Git 无模型语义实验见 历史私有实验，不是 G3 整体验收通过。

## 1. 输入材料化和保活

每个写 attempt 独立 worktree；协调器记录 Git common-dir、worktree 身份、基线 OID、来源配置连续性和依赖顺序。不能以分支名字替代不可变版本。

依赖就绪后，在专属准备区按计划顺序组合指定 commit；冲突时不释放消费者，改由 Codex 指派范围明确的冲突任务。冻结合成输入 commit，pin 所有输入 refs，在同一 plan revision 下提交消费者的 `base_commit`。实际消费者 tree 必须包含指定依赖。

若依赖被替换，正在运行的消费者不换底层文件，标记 stale；其候选不能按旧认可自动整合。更改计划不重置同 work 的累计预算。

候选以 commit OID 为规范身份，tree OID 为补充，工具私有 ref 保活。引用使用项目命名空间和精确 OID；删除 worktree 不同时解除仍用于审查、依赖、回调和恢复的对象 pin。

首版不自动复制未提交工作到新基线。不相关脏主树可继续独立任务；相关未提交输入必须由 Codex 先明确处理，禁止偷偷使用旧 HEAD。子模块、sparse checkout、未合并 index、rebase/merge/cherry-pick 等进行中状态默认不进入自动整合；需要这些能力时另列验证，不当成普通 clean tree。

## 2. 测试和最终审查

执行树和验证进程停止后盘点全部文件，区分正式变更、临时文件、ignored 内容和外部依赖。清理无用临时产物后形成 commit 候选。

最终验证从该 OID 的干净材料化目录执行，环境/构建输入按合同记录；需要的 ignored 配置不能暗中来自旧工作树。新生成测试输出登记在验证任务清单中。报告记录候选 OID、命令、退出码、结果、环境版本和报告摘要。

`TaskReview` 只认可单项成果；`IntegrationReview` 必须绑定：

```text
target_worktree_identity / target_ref / target_base_oid
final_candidate_oid / ordered_input_oids
validation_digest / plan_revision / review_revision
```

协调器调度来源 Host 在 integration worktree 中串行组合。解决冲突/修改业务代码由执行者完成；组合后重新测试、由 Codex 对最终候选审查。任一输入、代码、验证条件或目标基线变化均使旧最终审查失效。

## 3. 当前工作分支落地

整合暂存区不是最终交付的替代品。主控认可最终 candidate 后，在约定的当前工作分支自动落地；不 push。

### 3.1 前提与意图

每仓库 OS 互斥 + `IntegrationAttempt` 持久账本；记录操作 ID、目标 symbolic ref、expected HEAD、candidate OID、前置 index/tree 摘要、未跟踪/ignored 碰撞清单、审查 revision、Git 进程身份和结果。

前置检查：原目标 worktree 与分支身份不变，HEAD=target_base，tracked/index 干净，无进行中 Git 操作，候选可快进；全量检查候选将写入路径及其祖先中的 untracked/ignored 文件和文件/目录碰撞。不能只运行普通 status 判断。必要 Git 配置（如 autostash）通过显式保护参数中和，不修改配置文件。

本工具管理的写入者必须先交出该目标，Codex 确认其不在目标树写代码后才进入短整合窗口。持久化执行意图并建立 Git 子进程所有权，再运行：

```text
git merge --ff-only --no-autostash --no-overwrite-ignore <final-candidate-oid>
```

Go 以 argv 传参，OID 来自验证后的对象标识，不通过 shell 拼接。版本不支持这些保护参数时拒绝自动整合，不省略参数。遵守已有 Git hooks/签名等正常行为，不自行加 no-verify；hooks 产生额外写入或失败需要后置对账。

`update-ref` 不能代替以上工作树更新，因为它不会同步 index/worktree。禁止通过 reset/stash/覆盖复制补齐失败结果。

### 3.2 后置与崩溃矩阵

| 观测 | 动作 |
|---|---|
| 目标分支/HEAD仍是原值，index/tree符合前置状态，旧Git进程已结束 | 重新检查所有前提后可重试同一整合意图 |
| HEAD=candidate，分支/index/tree和交付后条件吻合 | 补记 integrated，不重复运行 merge |
| HEAD/index/tree混合、分支漂移、额外写入或旧进程身份未知 | integration_uncertain，保留候选/意图/现场，禁止自动reset/再次merge |

退出码为零不单独证明完成，非零也不证明没有写入。后置检查包括 hooks 和必要整合后验证。协调器重启后必须确认原 Git 进程已结束；DB 租约过期不是重新启动许可。

Git 不提供目标 symbolic ref、HEAD、index、文件的统一原子 CAS，外部编辑器也不遵循本工具锁。因此自动落地仅支持稳定、已让出的目标工作区；外部在预检查后 checkout/写入可能产生不确定结果。确定性故障测试要验证检测、保全和停止自动动作，而不是将事后发现宣传为“从未修改”。需要更强的并发外部编辑保证时，属于须另行设计的边界，不可暗中覆盖。

## 4. 资源归属与可恢复清理

目录按 task/attempt/scripts/docs/data/tmp 组织，根目录使用 macOS Application Support、Windows LocalAppData 原生目录接口；获取失败就报错，不回退到仓库、TempDir 或缓存目录。

每个由本工具管理的普通文件、worktree 和私有 ref 都采用：

```text
create_intent → created(identity/version) → classified/pinned
                                         → delete_intent → removed
                                         → retained / cleanup_pending
```

创建意图先 durable 提交；实际创建后补文件身份。进程在两者之间崩溃时，对存在物做身份/内容对账，不自动继承父目录的删除授权。Host 离线时自己的清单写 durable spool。执行者任意创建物在停止后扫描登记，包含 ignored 文件；归属不明的文件保留并报告。

删除前写意图，再用固定管理根目录句柄和不跟随链接的相对操作，重新核验每个路径组件、文件身份和已知内容版本；不使用单次 EvalSymlinks+前缀检查后递归删除。未知挂载点、symlink/junction/reparse point 和身份变化都阻止穿越。文件已不存在可补记 removed；同名已重建不能视为原文件删除。

整棵 worktree 删除前，全部 tracked/untracked/ignored 内容必须已分类且可回收，无活写入者、回调或依赖 pin。只有满足整体归属才能调用具体 `git worktree remove`；不使用全仓 prune/clean 或通配 refs 删除。私有 ref 删除携带 expected OID，若被改变则保留。

Git objects 位于共享 common-dir，按内容去重，也可能被用户引用；不能作为工具独占文件直接删除。任务只建立/解除自己的保活 refs，绝不按任务清单删 objects，也不触发全仓 GC；对象回收交给 Git 正常维护。refs解除前仍需检查本工具依赖，解除后对象并非立即消失，不将它算成未清理的临时脚本。

Windows 占用删除初次加 3 次重试，延迟为 1、5、30 秒；次数和下一步 durable 记录，重启不置零。耗尽后 cleanup_pending，等待明确释放/主控操作，不不断尝试。目录未彻底回收不宣称完全清理成功。

## 5. 保留、容量与完成

定义 `Pin(resource_id, owner, reason, release_condition)`，按资源粒度记录：活动执行/可变恢复现场pin绑定worktree；审查/下游输入/未ACK交付pin绑定不可变ref和report；整合不确定pin绑定实际候选、目标观测与相关现场。只有仍需要可变目录的引用才阻止worktree回收，不因一个OID引用保留整棵目录。

每次accepted/integrated/逐事件acked/下游结束/主控解决或放弃都在状态事务中更新pin。候选已安全材料化、执行树退出且不再需要可变恢复现场后可解除worktree pin；最终候选在integrated、必要ACK完成且无下游/恢复引用后进入报告保留期，期满解除其ref/report保留。未消费和uncertain不解除。清理回执只引用持久化清理记录，不反向pin将要删除的worktree，避免completed等cleanup而cleanup又等completed的环。

TTL 只回收已解除用途pin并满足到期条件的资源；保留期本身有明确截止时间，不是永久pin。新依赖重新引用前必须事务取得资源pin并证实它尚可用，不能与清理并发猜测存在。

初始默认：成功且已消费后的诊断日志保留 7 天、报告/任务回执保留 30 天；一次性脚本和 tmp 在不再需要时立即清理；失败现场保持 pin 直到主控明确解决/放弃，再进入 7 天回收期。用户交付代码不按 TTL 删除。容量不足拒绝新任务，不驱逐被 pin 材料。

业务交付成功与清理完成分别报告；`completed` 要求所有无用临时项 removed，保留项都有明确用途/保留期。未知垃圾不是永久合法 retained：工作流进入 awaiting_cleanup_decision，让主控分类；确认为无用且归属明确后必须删除。

## 6. 安装、升级和卸载

集成包有版本、文件摘要、配置条目 ID 和安装清单。只增删本项目条目；配置发生并发修改时按条目对账，不能整份恢复旧备份覆盖用户新配置。Hook 更新按 Codex 正常信任流程重审，不代批。

活动任务、Run Host、report/resume、待回调或未知进程仍引用旧二进制时，不覆盖/删除该版本。升级使用并存版本与协议兼容检查，既有工作流留在原版本；不兼容迁移推迟到其安全结束。

卸载先冻结新派发并列明活动依赖；Hook、Skill、集成配置和协议版本也纳入pin，尚需原会话恢复、回调/ACK或续接时不能先卸掉这些接入条目。只关闭新派发入口，保留必要接入与执行文件；直到所有依赖解除后再删除。默认保留被pin的数据和版本；显式purge也不能绕过活动资源所有权。清除剩余材料在任务结束并可恢复交付后执行，不强制杀死任务来制造“卸载完成”。

发布需包含用户选定的开源许可、依赖许可信息、版本矩阵和卸载说明；许可选择属于发布前维护者决定，不影响本轮架构评审，也不能在实际发布时省略。

## 7. 无模型实验与发布边界

子 Agent 在独立临时仓库已验证：update-ref 不同步工作树；merge 默认覆盖 ignored；merge.autoStash 配置会改变普通 merge 行为；普通 worktree remove 可删除 ignored 内容。这些证据决定保护协议，但不证明整合/清理实现已存在。

G3 必测：预检查后切分支/编辑文件、merge完成DB未记账、Git子进程仍活、ignored碰撞、祖先目录替换、登记崩溃窗口、Windows文件占用、临时ref被改指向。记录 Go/Git/OS/架构/文件系统版本；现有 macOS 实验不能替代 Windows 测试。
