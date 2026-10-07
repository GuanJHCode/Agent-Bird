# Resume CLI 接线交接

实施完成，源码冻结待 ROOT 独立审查/集成。工作区 `tmp/resume-cli-wiring`，branch `feat/resume-cli-wiring-20261008`，base `a6ecc08`。只修改授权的六个 CLI Go 文件；Store/Coordinator/contract、legacy source_presence 和 journal 源码未改。

## 行为

- `main.go` 的 observed-revision Resume 严格调用现有 `loadControlCapability`，再确认 Coordinator capability，再持久选择 journal，然后发带 command ID 的 Resume。ACK loss、非法 receipt、恢复错误保留 pending，同一 implicit 请求重用 ID。锁不跨 RPC，测试在 RPC 回调中再次实际选择同一个记录成功。
- `resume_control.go` 校验完整 exact/non-null JSON、重复/未知键、版本/模式/status、请求 hash、task/revision/command/run、cap 可用 LaunchID scope、owner、正 control revision/epoch/sequence、canonical UTC 时间。既有 owner 不读取 bootstrap、不查询 Summary/HostStatus、不启动 Host；完成 journal 后原样输出 payload。successor 授权 ID 不传给 Host。
- legacy 仅 implicit + authenticated remote `resume_command_not_supported` 回退原 Summary gate → ensureSourceHost → 空 ID Resume，严格校验 status-only receipt 后完成本地记录。该路径延续 legacy 非幂等语义，不声称远程 stable-ID replay。explicit 和其他错误不回退。
- `successor_presence.go` 在已校验 successor receipt 后加载/scope 核验 bootstrap；仅当前 command 相同、succession_pending、old_host_exited 启动一次。内存 Hello 仅加 ResumeCommandID，磁盘 bootstrap 保持原字节。intended/bound 只等待；已 dead 返回 `owner_resume_pending_blocked`；未知/reused 不启动；不同当前 command 只读结束。单原 5s deadline，继承更短 parent deadline；已取消时不触碰 bootstrap。
- `task_entry.go` 转发普通 Resume `--command-id`，显式空/非法 ID 拒绝；仅普通 Resume 跳过提前 bootstrap gate。高层真实 handle → 实际低层 flag parser → 实际 journal → callback RPC → receipt 分支在 bootstrap 缺失时成功。typed `--request` 和 collect 的原 bootstrap gate 保留。

ROOT 裁定：沿用现有 capability loader 契约，本批不全局强化其 hardlink/duplicate/inode 行为；确保该 loader 在 journal 前成功。缺 bootstrap 成功重放仅承诺 existing_owner，successor 仍需 bootstrap/scope 检查。

## 证据

Go `go1.26.8 darwin/arm64`，固定工具目录 `tasks/g0-macos/data/go/bin`。普通测试使用 `TMPDIR=/private/tmp`；新测试不启动服务、Coordinator、Provider 或 native Host，外部阶段使用纯回调，filesystem/journal/flag parser 使用实际实现。

| 证据 | package P/F/S | top P/F/S | named P/F/S | 含义 |
| --- | --- | --- | --- | --- |
| data/api-red.json | 0/1/0 | 0/0/0 | 0/0/0 | 缺私有 API，build-fail；不计行为 RED |
| data/behavior-red.json | 0/1/0 | 1/6/0 | 14/82/0 | 编译桩 + 原 decode/高层 bootstrap gate 的真实行为失败 |
| data/cancel-red.json | 0/1/0 | 9/1/0 | 124/1/0 | 已取消恢复仍读取缺 bootstrap 的回归 RED |
| data/final-focused.json | 1/0/0 | 33/0/0 | 206/0/4 | 最后六 Go 文件快照 |

final 新 CLI/successor 子集：10 top、128 named PASS，0 FAIL、0 SKIP；原 journal 58 PASS/4 UID SKIP；其余相关旧单元测试 20 named PASS。`data/counts.json` 从实际 Go JSON action 计数，named 包含 top 与 subtest，不等同叶子数。四个 SKIP 精确是 `TestResumeJournalRejectsForeignOwnership/{state,directory,lock,record}`，本机更改 foreign UID 的 chown 返回 EPERM；并非跳过本次新断言。

最终命令（模块内，退出码 0）：

```sh
TMPDIR=/private/tmp /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go test -json -count=1 -run '^Test(Resume(CLI|Successor|Journal)|CoreEnsureSourcePresence|CoreSourceStartupRejectsCanceled|Task(SubmitRejectsOwnerOverrides|SubmitExistingHandleNeverResubmits|RunMissingWorkspaceDoesNotBindOrCreateHandle|HandleScopeRejectsForeignTaskBeforeReadingControl|HandleNeverAcceptsControlOverride)|Bootstrap(V2DiskHasNoTokenFields|ControlReconstructionAndLegacyReadOnly))' ./cmd/orchestrator
TMPDIR=/private/tmp /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go vet ./cmd/orchestrator
```

RED/中间 GREEN selector：api-red 为 `^TestResumeCLI`；其余为 `^TestResume(CLI|Successor)`。保留各阶段独立 JSON/stderr，以及空输出 exit0 的 `data/vet.log`。最终 `gofmt -l` 六文件无输出、`git diff --check` 无输出。六文件 SHA256 见 `data/source-sha256.json`；之后只整理 docs/data。

## 未验收与清理

ROOT 持 race/native/container/最终资源，因此本子任务不运行 full suite、race、Linux、真实 Unixsocket 或原 task Resume native 端到端；callback 与实际 filesystem 单元证据不冒充以上验收。独立 CLI 审查尚待 ROOT 安排，服务端 admission 属另一分支，不在本提交内声称验收。journal UID 矩阵由 ROOT 独立补验。

仅删除本任务创建且为空的 `tasks/resume-cli-wiring/tmp`；保留计划、报告、RED/最终日志与 hash/counts 交付物，未删除归属不明文件。未 merge/install/push。
