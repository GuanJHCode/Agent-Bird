# 显式单 producer 分支返工

Task8 在既有 dependency vector、retry decision 和 attempt binding 上增加局部返工。产品最终提交 `44dd1e3`；前置基线 `5dd987c`。没有新增数据库版本、独立调度队列或目标锁表。

## 可用范围

- `ReworkSpec.version` 省略或为 `0`：保留原 Claude 四节点线性链、原 receipt JSON 和 revision digest。`version: 1` 才授权单 producer 的完整 dependent closure；其它版本拒绝。
- v1 从持久 DAG 选取 root 的全部下游，最多 64 节点，稳定拓扑顺序。独立 producer 和其 accepted 输入保持原 revision、事件、attempt 与 budget。
- 支持 pinned Claude／AGY implementer 与 readonly reviewer，以及 candidate validate／integrate。所有依赖仍须 owner review；多输入节点必须使用 Task5 的显式 v1 vector。内容仍由唯一 `source_task` 选择，没有隐式 merge。
- 闭包内第二个 workspace producer、已启动过的 integration、不支持的 descendant、fallback、active／UNKNOWN、开放问题、未结算预算、缺失历史证明均原子拒绝。首批 final integration 使用原 authorizing review 的目标证明；同一闭包不能预约两个同目标 integration。
- 每个节点都有固定下一 segment 的正数时间 hold，包括无 execution policy 的任务。attempt/group/time 不重置；claim 原子消费 binding 并转入 budget reservation；取消、disable、确定依赖终止释放未消费 hold并保留历史字节。旧无 policy 的零 hold/FIFO 路径保持。

## 证据与目标

v1 保存旧 revision、原/revised adapter、依赖、历史 Host event、原 capture、外部 accepted tuple 和 target proof。`RevisionSHA256` 继续绑定单 producer 的 `ReworkContext`；新增 `ClosureSHA256` 绑定完整闭包决策。pending、consumed replay、ACK 丢失都使用原记录，不重新抓取历史或补造缺失 binding。

root 可以是 review 的非内容依赖，但必须通过原 capture 的 accepted 输入链证明可达。新 revision 的 validate/review 接受及 integrate 派发会比较原 Host 证明中的仓库 common directory、ref、target directory kernel identity；目标漂移不会迁移预约或隐式退款。

排他范围为 `(Candidate.CommonDir, TargetRef)`；不同路径不会拆分同一范围。active／UNKNOWN 持续占有；较早 v1 promise不会被后来变为 ready 的普通任务抢占。后到同目标 v0 rework在写入前拒绝；不同 ref 的 v0/v1 可按任一入队顺序完成。无法证明的 legacy scope保持阻塞。

目标预约从未退休 binding开始枚举，以 immutable decision里的节点判定 operation，再核对 current revision/revised payload。request version、JSON内容或当前 task payload损坏，均不能让预约被当作不存在。原 Git common-directory flock、ref/base CAS 与 Host journal仍承担实际 mutation 的最后校验。

## 审查与原始失败

独立审查覆盖历史 evidence、legacy binding/replay、provider边界、预算、quota与目标生命周期。初稿 `ad21140` 的目标预约顺序与重复 integration问题被拦截；后续修复补上后到 legacy冲突、损坏 decision和漂移 task payload导致预约失踪。最终 `44dd1e3` 相关专项复审 PASS；历史/proof修复在 `1c5032a` 独审 PASS，最后改动仅扩展目标预约枚举。

原 RED 和全量失败日志均保留在 `../data/`。其中 `red-version` / `red-closure` 是原功能缺口；`red-target-promise`、`red-duplicate-target`、`red-later-legacy`、`red-corrupt-promise`、`red-task-payload-marker` 是已复现并修复的预约问题。`normal-all` 的 4 个 legacy子项失败来自错误分类兼容性，后续修复；没有删除断言或覆盖原失败日志。

## 验收边界

Store 测试使用真实 SQLite、事件/退出/接受/claim API和受控 artifact。测试内的 Host receipt、provider profile与路径别名是协议夹具，不能独自证明真实模型、真实路径别名或真实多仓库运行。

macOS CLI测试运行真实 CLI、Coordinator、Host、Git worktree、审查/owner decision/返工/集成，以受控协议进程替换模型。Git专项另覆盖实际 common-directory锁、受保护 fast-forward、目标漂移及不确定 outcome。没有执行模型账户、用户安装、push或release。

Linux arm64验证仅覆盖本批 Store/文件系统逻辑。Linux Provider/完整安装能力、Intel Mac、Windows原生验收、真实模型组合仍未验证。Codex/Grok新权限和既有 public guard保持原边界。多个 workspace producer的联合返工、一般 compose及已集成后选择新 base仍属后续工作。

## 最终验证记录

以下最后变更后的证据均针对产品 `44dd1e3`，FAIL/SKIP 均为 0。具名数量包含顶层和子项，不将重复运行相加。

| 范围 | 顶层 / 具名 PASS | 原始日志 SHA256 |
|---|---:|---|
| Store 全量 | 302 / 827 | `ad16b74c8ea9009a1a727e8c31786732243cb85bce9bade840dc09bc43c129d9` |
| 最终受影响 race | 40 / 105 | `c5188cc897c7bced6425a3c140e7dae8709a3ef23555a17f10171a1e2739e5f4` |
| 真实 CLI 新旧返工 | 2 / 2 | `0a43b46d282d4edf0552027d538926e9c1d367d4d8671a4e726a7d83392b741b` |
| 实际 Git 锁与目标漂移 | 4 / 4 | `1b3fd54593aed3e1bfa3886b2b3e8bf59c94d3cd63bed66b5c1ab266e7b506a3` |
| Linux arm64 Store | 18 / 60 | `b61a4323139bf7b573c60d7e05ee895369d9c7b525b8ce76d0323e84d0582017` |

更早的广域 race 在 `cbf08c3` 为 101 顶层 / 249 具名 PASS（raw `9b6fc9a91d91426a43d48f62a305c4bc1f1fcaebfaedf1df4ca2afe04f6fb173`）；后续仅目标预约/legacy分类改动，受影响范围在最终40/105 race中重新验证。它们是不同快照证据，不能相加成额外测试。

最终 module-local `go build ./...`、`go vet ./...` 与 `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/store` 均 exit0，见 `static-final-manifest.json`。Linux未运行race：使用现有CGO=0交叉编译产物及固定runtime镜像，镜像没有Go/C构建工具链。Mac race覆盖上表范围。

Linux二进制 SHA256 `c936ce99c29f5c35f89288c34dbad74f776abff6c168d1840e58e5f0d466b815`，固定镜像 `sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`。原CID `34edbb7160ee140e91479c0d7e8c0c73b21404b1567c12f334e45bc870e96986` 已按精确ID inspect确认不存在；禁网、非root、只读root、只挂测试二进制、没有超时或强制清理。

全部命令、原始输出、逐项结果及CID在 `../data/`；复现脚本在 `../scripts/`。最终Linux二进制留在任务私有 `../tmp/linux-bin/` 作为原物理证据，不作为发行产物。没有创建新的用户安装或外部环境。
