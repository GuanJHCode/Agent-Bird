# Resume CLI durable request journal 实施报告

已完成独立journal底层实现、TDD与本机聚焦验收；五个Go文件已冻结给ROOT独立安全审查。尚未完成CLI/RPC接线、独立审查结论与共享最终验收，不将本地journal的completed状态当作Store receipt或权限证明。

## 交付与位置

- `cmd/orchestrator/resume_journal.go:25`：request字段Version、State、ControlFile、TaskID、WorkRevision、CommandID。没有token/receipt字段，不读cap内容。
- `:34`：`resumeJournalAmbiguityError{CommandIDs []string}`，sorted ID choices并JSON escaped Error，无token。
- `:58`：`selectResumeJournalRequest(state, controlFile, taskID string, revision int, commandID string) (resumeJournalRequest,error)`。
- `:128`：`completeResumeJournalRequest(state string, request resumeJournalRequest) error`。
- `:188/:212`：canonical existing state→child descriptor绑定、私有目录与.lock命名inode校验、FlockEX|NB busy明确拒；锁仅覆盖本地scan/select/create/complete，返回时已释放，不跨RPC。
- `:282/:310/:347`：全records扫描与snapshot复验，16KiB bounded strict canonicalJSON+newline，0600/regular/euid/nlink1；hash(commandID) filename；坏/未知/替换/symlink/hardlink/mode/size/metadata异常failclosed，不静默跳过。
- `:394/:443`：state FD相对exclusive temp，filefsync；异常cleanup仅本调用已证明same inode/stat的temp；不按prefix清理未知遗留。
- `:466/:483/:515`：pending原子exclusive publication、complete原子replace、两目录fsync、最终body/inode/目录复验；发布成功后的pending不因后续错误删除。
- `resume_journal_publish_darwin.go:8` / `resume_journal_publish_linux.go:8`：跨FD RENAME_EXCL / RENAME_NOREPLACE，参考repo internal/install已有package-private模式；`resume_journal_publish_other.go:5`：unsupported明确拒。没有copy/link fallback、没有nlink2窗口。
- `resume_journal_test.go:56` lifecycle；`:104` explicit/cross-tuple/ambiguity；`:139` canonical path/historical completed cap删除；`:167`真实子进程静默exit23后implicit复原；`:213`128B/UTF8/NUL/trim/punctuation/Unicode边界；`:239`missing/tuple complete拒绝；`:262`busy与12个并发从零pending生成并复用同一个新pending；`:333`实际filesystem安全矩阵（含FIFO）；`:433`六个真实publication crash窗口；`:539`实际inode/stat drift；`:589`UID变更矩阵；`:620`不覆盖已有ID；`:662`发布后失败保留pending并恢复。

路径均相对 `tools/orchestrator`。仅这些新Go文件和本任务plans/docs/data；未修改main.go/task_entry.go、Store、共享contract或其他worktree；未派生、集成、安装、push。ROOT将CLI接线于独立owner-public-wiring WT。

## 冻结语义与调用方契约

implicit在全部严格健康records中按canonical absolute ControlFile/TaskID/WorkRevision找pending：0生成随机ID并exclusive durable create，1复用，>1 typed ambiguity；completed永不implicit复用。explicit仅same ID/full tuple replay，健康目录没有ID可以新增，即使已有其他pending。CommandID与Store相同非空UTF8、<=128B、无NUL、trim exact；内部tab/newline及public punctuation/Unicode不因filename收窄。filename是sha256(CommandID)，不以public ID拼路径。

ROOT裁定：当次ControlFile Abs+EvalSymlinks规范化，但caller必须先loadControlCapability严格鉴权，leaf symlink不是本helper的cap认证。历史record.ControlFile仅absolute+clean/语法，不要求历史文件仍存在。既有record不静默改tuple/path。complete只能更新存在的exact ID/tuple，不创建missing；completed只记录本地状态，实际explicit replay仍必须请求Store。

state及resume-requests分别0700/euid，.lock0600/nlink1/euid/zero/openatNOFOLLOW+FlockNB；目录/lock命名inode与held descriptor前后复验，records全过程snapshot元数据复验。unknown或坏record即使与当次请求无关也阻断选择/完成；.lock是唯一records域保留非JSON项。

## 崩溃与durability裁定

最初直接exclusive final record→write，以及child内complete temp，会在abrupt crash时产生坏record/unknown temp，阻断implicit。真实faultpoint RED证明这个问题，随后ROOT批准把两类temp都放held state FD，位于records扫描域外：`.resume-journal-pending-<random>`、`.resume-journal-complete-<random>`。filefsync后才atomic跨FDpublish/replace，成功后fsync source state与destination child，再复验identity。跨FS或unsupported错误安全返回，不copy迁移。

六个子进程故障点为pending/complete各自的temp_created、temp_synced、record_published或record_renamed。private WithSteps callback只用于受控测试，公开内部wrapper传nil；production不读取任何fault env。测试实际exit37、无stdout/stderr，核对创建点temp size0、同步点full body非空、mode0600/euid/nlink1、orphan位置，再由新调用implicit读取：未发布pending生成新请求；发布pending复用原ID；完成前复用原pending；rename后不implicit复用completed而生成新请求。state orphan原样保留，不进入records扫描域。没有宣称真实断电或持久介质故障验证。

普通错误仅清理本调用已知exclusive temp inode，目录或命名身份不确定则保留。后续GC必须独立授权且不能凭prefix认定归属；本批未实现GC。成功publish后错误的测试实际加入unknown marker使最终检查失败，观察原pending仍存在；仅测试删除自己marker后implicit重放同一ID。

## 验证证据与准确计数

工具链 `data/toolchain.log`：`go version go1.26.8 darwin/arm64`。下列Go命令位于`tools/orchestrator`，使用pinned Go PATH与`TMPDIR=/private/tmp`，所有测试`-json -count=1`，JSON保存在本任务data。

| 证据 | 命令/性质 | package P/F/S | top P/F/S | named P/F/S |
|---|---|---|---|---|
| `red-behavior.jsonl` | 4个happy-path selector（完整命令见下方）；可编译API scaffold返回not_implemented，4个行为失败，不是编译缺API | 0/1/0 | 0/4/0 | 0/4/0 |
| `green-initial.jsonl` | `go test -json -count=1 ./cmd/orchestrator -run '^TestResumeJournal'`；初实现，中间证据 | 1/0/0 | 8/0/0 | 40/0/0 |
| `red-publication-crash.jsonl` | `go test -json -count=1 ./cmd/orchestrator -run '^TestResumeJournalPublicationCrashWindows$'`；4 child真实filesystem行为失败，parent失败 | 0/1/0 | 0/1/0 | 2/5/0 |
| `green-atomic-publication.jsonl` | 全journal selector，中间证据 | 1/0/0 | 11/0/0 | 54/0/4 |
| `final-focused.jsonl` | 全journal selector，中间证据（FIFO/并发首次创建补测前） | 1/0/0 | 13/0/0 | 56/0/4 |
| `final-focused-v2.jsonl` | `go test -json -count=1 ./cmd/orchestrator -run '^TestResumeJournal'`；最后测试修改后完整退出0、elapsed2.326s | 1/0/0 | 13/0/0 | 58/0/4 |

RED happy-path 完整命令：

```sh
go test -json -count=1 ./cmd/orchestrator -run '^TestResumeJournal(Lifecycle|ExplicitConflictAndAmbiguity|CanonicalControlAndHistoricalPath|CrashKeepsImplicitRequest)$'
```

named含父/子测试，不能称58个独立顶层测试；共62个named完成，58PASS/4SKIP/0FAIL。SKIP仅ForeignOwnership/state、directory、lock、record，真实os.Chown遇EPERM，缺privileged环境，未伪造owner反例通过。ROOT已明确安排受控capCHOWN matrix补验。

`go vet ./cmd/orchestrator` exit0（`data/vet.log`空，无诊断），针对最后生产source；后续只改变tests，source证据可复用。`gofmt -l`五个Go文件输出空；staged `git diff --check` exit0。未经授权未跑full/race/Linux/native、Coordinator/Host/RPC或真实Provider；真实子进程仅当前Go test executable的普通unit helper。

## 状态与剩余缺口

已完成：窄API、无token durable journal、完整implicit/explicit/ambiguity/complete语义、descriptor/lock/strict record安全措施、跨FD原子发布与双目录fsync、实际filesystem/子进程TDD、focused验证与vet。未完成：ROOT独立安全审查结论、CLI/RPC接线、Store整链集成与公共最终验收；未验证：4个UID变更实际拒绝、Linux publisher执行、race/full/native、真实断电。源码已冻结，任何审查导致必须修改将先告知ROOT。

测试临时目录由Go清理（inode-drift backup亦task-owned cleanup），没有保留待清理的本任务无用临时文件；保留plan/report与必要JSON/toolchain/vet证据。实际运行期crash产生的state-prefix temp保留，属于已明确的后续GC边界，不自动删除未知文件。
